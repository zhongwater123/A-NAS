package assistantapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// BrokerChanges reads and decides pending changes through the spike's change
// broker (ai/assistant-spike/change_broker.py) on a Unix socket. It exists for
// the UI prototype only: in A-NAS the Product Service stores pending changes
// itself and the File Broker applies a confirmed one as the member.
func BrokerChanges(socket string) ChangeStore {
	return &brokerChanges{client: &http.Client{
		Timeout: 90 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}}
}

type brokerChanges struct {
	client *http.Client
}

func (b *brokerChanges) List(ctx context.Context, account string) (json.RawMessage, error) {
	return b.do(ctx, http.MethodGet, "/changes", account, nil)
}

func (b *brokerChanges) Decide(ctx context.Context, account, id string, confirm bool) (json.RawMessage, error) {
	action := "reject"
	if confirm {
		action = "confirm"
	}
	return b.do(ctx, http.MethodPost, "/changes/"+id+"/"+action, account, nil)
}

func (b *brokerChanges) Attach(ctx context.Context, account, session string, submission json.RawMessage) error {
	body, err := json.Marshal(map[string]any{"sessionId": session, "submission": submission})
	if err != nil {
		return err
	}
	_, err = b.do(ctx, http.MethodPost, "/changes/attach", account, body)
	return err
}

func (b *brokerChanges) do(ctx context.Context, method, path, account string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://broker"+path+"?account="+url.QueryEscape(account), reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := b.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	reply, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	switch response.StatusCode {
	case http.StatusOK:
		return reply, nil
	case http.StatusNotFound:
		return nil, ErrChangeNotFound
	case http.StatusConflict:
		return nil, ErrChangeDecided
	default:
		return nil, fmt.Errorf("change broker answered %d", response.StatusCode)
	}
}
