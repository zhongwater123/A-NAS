package assistantapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

// ChangeStore is the executor of 提交更改 (ADR 0024 decision 4). It checks a
// submission and copies what it would write, which is what the member's card
// shows; it records the member's one-time grant; and it applies a granted
// change when the assistant's MCP tool asks, as the member. Every call carries
// the member's session (accounts.SessionToken) and the executor verifies it
// itself, so the Product Service cannot grant anything for a member who is not
// signed in (decision 10). In A-NAS the File Broker executes; the spike's
// change broker stands in for it.
type ChangeStore interface {
	// Prepare turns the arguments of one call of 提交更改 into a change
	// awaiting the member. ErrInvalidSubmission when it cannot be applied.
	Prepare(ctx context.Context, account string, submission Submission) (Change, error)
	// Decide moves a change on: approve records the grant, reject and expire
	// end it. ErrChangeNotFound, or ErrChangeDecided when it moved already.
	Decide(ctx context.Context, account, id string, decision Decision) (Change, error)
	// Get returns a change as it is now, such as after the tool applied it.
	Get(ctx context.Context, account, id string) (Change, error)
}

// Submission is one call of 提交更改 as Hermes reported it starting.
type Submission struct {
	SessionID string
	RequestID string
	// Args are the call's arguments: the summary and the operations.
	Args json.RawMessage
}

type Decision string

const (
	Approve Decision = "approve"
	Reject  Decision = "reject"
	Expire  Decision = "expire"
)

// Change is the executor's view of a change: what the card shows.
type Change struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	// Raw is the whole change as the executor described it.
	Raw json.RawMessage `json:"-"`
}

var (
	ErrChangeNotFound    = errors.New("change not found")
	ErrChangeDecided     = errors.New("change already decided")
	ErrInvalidSubmission = errors.New("submission cannot be applied")
	// ErrChangeSession reports that the executor did not accept the member's
	// session.
	ErrChangeSession = errors.New("the member's session was not accepted")
)

var changeIDPattern = regexp.MustCompile(`^[0-9a-f]{8}$`)

// SessionHeader carries the member's session token to the executor.
const SessionHeader = "X-A-NAS-Session"

// BrokerChanges is the spike's change broker (ai/assistant-spike/change_broker.py)
// on its product socket, standing in for the File Broker.
func BrokerChanges(socket string) ChangeStore {
	return &brokerChanges{client: &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}}
}

type brokerChanges struct {
	client *http.Client
}

func (b *brokerChanges) Prepare(ctx context.Context, account string, submission Submission) (Change, error) {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(submission.Args, &args); err != nil || args == nil {
		return Change{}, fmt.Errorf("%w: the arguments are not an object", ErrInvalidSubmission)
	}
	body := map[string]any{"sessionId": submission.SessionID, "requestId": submission.RequestID}
	for _, key := range []string{"summary", "operations"} {
		if value, ok := args[key]; ok {
			body[key] = value
		}
	}
	payload, _ := json.Marshal(body)
	return b.do(ctx, account, http.MethodPost, "/changes/prepare", payload)
}

func (b *brokerChanges) Decide(ctx context.Context, account, id string, decision Decision) (Change, error) {
	if !changeIDPattern.MatchString(id) {
		return Change{}, ErrChangeNotFound
	}
	return b.do(ctx, account, http.MethodPost, "/changes/"+id+"/"+string(decision), nil)
}

func (b *brokerChanges) Get(ctx context.Context, account, id string) (Change, error) {
	if !changeIDPattern.MatchString(id) {
		return Change{}, ErrChangeNotFound
	}
	return b.do(ctx, account, http.MethodGet, "/changes/"+id, nil)
}

func (b *brokerChanges) do(ctx context.Context, account, method, path string, body []byte) (Change, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://broker"+path+"?"+url.Values{"account": {account}}.Encode(), reader)
	if err != nil {
		return Change{}, err
	}
	request.Header.Set(SessionHeader, accounts.SessionToken(ctx))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return Change{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return Change{}, err
	}
	switch response.StatusCode {
	case http.StatusOK:
		var change Change
		if err := json.Unmarshal(data, &change); err != nil || change.ID == "" {
			return Change{}, fmt.Errorf("change broker answered unexpectedly: %s", truncate(data))
		}
		change.Raw = data
		return change, nil
	case http.StatusNotFound:
		return Change{}, ErrChangeNotFound
	case http.StatusConflict:
		return Change{}, ErrChangeDecided
	case http.StatusUnprocessableEntity:
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		return Change{}, fmt.Errorf("%w: %s", ErrInvalidSubmission, failure.Error)
	case http.StatusUnauthorized, http.StatusForbidden:
		return Change{}, ErrChangeSession
	default:
		return Change{}, fmt.Errorf("change broker answered %d: %s", response.StatusCode, truncate(data))
	}
}

func truncate(data []byte) string {
	if len(data) > 300 {
		return string(data[:300]) + "…"
	}
	return string(data)
}
