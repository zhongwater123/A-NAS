// Command broker-spike runs the File Broker's executor of the assistants'
// changes (提交更改, ADR 0024 decision 4) in the assistant spike
// (docs/research/hermes-agent-runtime.md): the real
// internal/filebroker/rootbroker server as the Host Agent runs it, so the
// spike, its real-model scenarios and the UI prototype measure the product's
// rules rather than a copy of them.
//
//	broker-spike DATA STATE PRODUCT_SOCKET AGENT_SOCKET
//
// AGENT_SOCKET is bound into every assistant sandbox and answered exactly as
// on an A-NAS. PRODUCT_SOCKET keeps the spike's product protocol, which
// scenarios.py, approvals-broker.sh and the Product Service in development
// (assistantapi.BrokerChanges) speak: HTTP with the member in ?account=. Each
// request becomes the File Broker's own call on file-broker.sock beside it.
// Only root, or SPIKE_PRODUCT_UID for PRODUCT_SOCKET, can reach the two.
//
// Accounts come from the container's passwd and group files, where A-NAS asks
// the Product Service: an account in the A-NAS UID range in a-nas-users, and
// in a-nas-admins for an administrator. A session token is the account's name.
// The data volume's reserve is checked as on an A-NAS; SPIKE_BROKER_NO_RESERVE=1
// turns the check off on a small disk.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filebroker"
	"github.com/zhongwater123/A-NAS/internal/filebroker/rootbroker"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/filesystem"
)

func main() {
	// The broker starts its file workers and its identity probe by running
	// this binary again, as the Host Agent does (cmd/anas-host-agent).
	if len(os.Args) == 3 && os.Args[1] == rootbroker.WorkerArgument {
		if err := rootbroker.RunWorker(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "file worker:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == rootbroker.ProbeArgument {
		if err := rootbroker.RunProbe(); err != nil {
			fmt.Fprintln(os.Stderr, "file worker probe:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: broker-spike DATA STATE PRODUCT_SOCKET AGENT_SOCKET")
		os.Exit(2)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger, os.Args[1], os.Args[2], os.Args[3], os.Args[4]); err != nil {
		logger.Error("broker-spike stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, data, state, productSocket, agentSocket string) error {
	config := rootbroker.Config{
		Sessions:   passwd{},
		Accounts:   passwd{},
		VolumeRoot: data,
		VolumeReady: func() bool {
			info, err := os.Stat(data) //nolint:gosec // G703: the data volume, named on root's own command line (assistant-install.sh).
			return err == nil && info.IsDir()
		},
		AssistantChanges: state,
		Logger:           logger,
	}
	if os.Getenv("SPIKE_BROKER_NO_RESERVE") == "1" {
		config.AssistantChangesAdmit = func(string, int64) (bool, error) { return true, nil }
	}
	server, err := rootbroker.NewServer(config)
	if err != nil {
		return err
	}
	productUID := 0
	if raw := os.Getenv("SPIKE_PRODUCT_UID"); raw != "" {
		if productUID, err = strconv.Atoi(raw); err != nil {
			return fmt.Errorf("SPIKE_PRODUCT_UID: %w", err)
		}
	}
	brokerSocket := filepath.Join(filepath.Dir(productSocket), "file-broker.sock")
	broker, err := listen(brokerSocket, 0o600, 0)
	if err != nil {
		return err
	}
	agent, err := listen(agentSocket, 0o666, 0)
	if err != nil {
		return err
	}
	product, err := listen(productSocket, 0o600, productUID)
	if err != nil {
		return err
	}
	failed := make(chan error, 3)
	go func() { failed <- server.Serve(broker) }()
	go func() { failed <- server.ServeAssistantChanges(agent) }()
	go func() {
		handler := newProduct(filebroker.NewClient(brokerSocket), config.Sessions)
		failed <- (&http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}).Serve(product)
	}()
	stop, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	select {
	case err := <-failed:
		return err
	case <-stop.Done():
		return nil
	}
}

// listen makes a socket only owner may connect to, with mode.
//
//nolint:gosec // G703: the broker's own sockets, named on root's own command line (assistant-install.sh).
func listen(path string, mode os.FileMode, owner int) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chown(path, owner, -1); err != nil {
		_ = listener.Close()
		return nil, err
	}
	if err := os.Chmod(path, mode); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// passwd finds members in the container's passwd and group files.
type passwd struct{}

func (passwd) ResolveSessionIdentity(_ context.Context, token string) (accountid.Identity, error) {
	found, err := user.Lookup(token)
	if err != nil {
		return accountid.Identity{}, accountid.ErrSessionNotFound
	}
	identity, err := member(found)
	if err != nil {
		return accountid.Identity{}, accountid.ErrSessionNotFound
	}
	return identity, nil
}

func (passwd) ResolveAccountUID(_ context.Context, uid int) (accountid.SessionUser, error) {
	found, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return accountid.SessionUser{}, accountid.ErrUserNotFound
	}
	identity, err := member(found)
	if err != nil {
		return accountid.SessionUser{}, accountid.ErrUserNotFound
	}
	return accountid.SessionUser{UserID: "user:" + identity.Username, Username: identity.Username, Role: identity.Role}, nil
}

var errNotMember = errors.New("not an A-NAS member")

func member(account *user.User) (accountid.Identity, error) {
	groups, err := account.GroupIds()
	if err != nil {
		return accountid.Identity{}, err
	}
	return identity(account, groups)
}

// identity is the A-NAS account a passwd entry with these groups stands for.
func identity(account *user.User, groups []string) (accountid.Identity, error) {
	uid, err := strconv.Atoi(account.Uid)
	if err != nil || uid < accountid.FirstUserUID || uid > accountid.LastUserUID ||
		!slices.Contains(groups, strconv.Itoa(accountid.UsersGID)) {
		return accountid.Identity{}, errNotMember
	}
	role := accountid.RoleMember
	if slices.Contains(groups, strconv.Itoa(accountid.AdminsGID)) {
		role = accountid.RoleAdmin
	}
	return accountid.Identity{Username: account.Username, UID: uid, Role: role, Enabled: true}, nil
}

// changes is what the product protocol asks of the File Broker
// (filebroker.Client).
type changes interface {
	PrepareChange(ctx context.Context, sessionID, requestID string, arguments json.RawMessage) (json.RawMessage, error)
	DecideChange(ctx context.Context, id, decision string) (json.RawMessage, error)
	GetChange(ctx context.Context, id string) (json.RawMessage, error)
}

// product answers the spike's product protocol through the File Broker,
// signed in as the account the request names. The File Broker lists no
// changes, so product keeps the ones it prepared for GET /changes.
type product struct {
	broker   changes
	sessions rootbroker.SessionResolver
	mu       sync.Mutex
	made     map[string][]string
}

func newProduct(broker changes, sessions rootbroker.SessionResolver) http.Handler {
	p := &product{broker: broker, sessions: sessions, made: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /changes/prepare", p.prepare)
	mux.HandleFunc("POST /changes/{id}/{decision}", p.decide)
	mux.HandleFunc("GET /changes/{id}", p.get)
	mux.HandleFunc("GET /changes", p.list)
	return mux
}

// signedIn is the request's context with the account it names as the session.
func signedIn(r *http.Request) (context.Context, string) {
	account := r.URL.Query().Get("account")
	return accountid.WithSessionToken(r.Context(), account), account
}

func (p *product) prepare(w http.ResponseWriter, r *http.Request) {
	ctx, account := signedIn(r)
	var body map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&body); err != nil {
		reply(w, http.StatusBadRequest, map[string]string{"error": "请求不是有效的 JSON 对象"})
		return
	}
	// The call's arguments, as the Product Service passes them on: its
	// summary and operations, as they came.
	arguments := map[string]json.RawMessage{}
	for _, key := range []string{"summary", "operations"} {
		if value, ok := body[key]; ok {
			arguments[key] = value
		}
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		p.fail(w, err)
		return
	}
	card, err := p.broker.PrepareChange(ctx, text(body["sessionId"]), text(body["requestId"]), raw)
	if err != nil {
		p.fail(w, err)
		return
	}
	var made struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(card, &made) == nil && made.ID != "" {
		p.mu.Lock()
		p.made[account] = append(p.made[account], made.ID)
		p.mu.Unlock()
	}
	replyRaw(w, card)
}

// text is a string field of the request, or empty.
func text(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

func (p *product) decide(w http.ResponseWriter, r *http.Request) {
	ctx, _ := signedIn(r)
	decision := r.PathValue("decision")
	if decision != "approve" && decision != "reject" && decision != "expire" {
		reply(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	card, err := p.broker.DecideChange(ctx, r.PathValue("id"), decision)
	if err != nil {
		p.fail(w, err)
		return
	}
	replyRaw(w, card)
}

func (p *product) get(w http.ResponseWriter, r *http.Request) {
	ctx, _ := signedIn(r)
	card, err := p.broker.GetChange(ctx, r.PathValue("id"))
	if err != nil {
		p.fail(w, err)
		return
	}
	replyRaw(w, card)
}

// list is the account's changes this process prepared, newest first, as they
// are now; one the File Broker no longer keeps is left out.
func (p *product) list(w http.ResponseWriter, r *http.Request) {
	ctx, account := signedIn(r)
	if _, err := p.sessions.ResolveSessionIdentity(ctx, account); err != nil {
		p.fail(w, err)
		return
	}
	p.mu.Lock()
	ids := slices.Clone(p.made[account])
	p.mu.Unlock()
	cards := []json.RawMessage{}
	for _, id := range slices.Backward(ids) {
		card, err := p.broker.GetChange(ctx, id)
		if errors.Is(err, wire.ErrChangeNotFound) {
			continue
		}
		if err != nil {
			p.fail(w, err)
			return
		}
		cards = append(cards, card)
	}
	reply(w, http.StatusOK, map[string][]json.RawMessage{"changes": cards})
}

// fail answers with the status the spike's callers read: 422 for a refused
// submission, with the File Broker's reasons, 409 for a change already
// decided, 404 for an unknown one, 401 for an account that is no member.
func (p *product) fail(w http.ResponseWriter, err error) {
	if refused, ok := errors.AsType[*wire.Error](err); ok {
		status := map[string]int{
			wire.CodeChangeInvalid:   http.StatusUnprocessableEntity,
			wire.CodeChangeDecided:   http.StatusConflict,
			wire.CodeNotFound:        http.StatusNotFound,
			wire.CodeUnauthenticated: http.StatusUnauthorized,
		}[refused.Code]
		if status == 0 {
			status = http.StatusInternalServerError
		}
		message := refused.Message
		if message == "" {
			message = refused.Code
		}
		reply(w, status, map[string]string{"error": message})
		return
	}
	if errors.Is(err, filesystem.ErrForbidden) || errors.Is(err, accountid.ErrSessionNotFound) {
		reply(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	reply(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func reply(w http.ResponseWriter, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		status, data = http.StatusInternalServerError, []byte(`{"error":"reply failed"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func replyRaw(w http.ResponseWriter, card json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(card)
}
