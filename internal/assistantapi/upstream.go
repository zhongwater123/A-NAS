package assistantapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/coder/websocket"
)

// Connector reaches the instance of the member signed in on the context's
// session (accounts.SessionToken). In A-NAS the File Broker is the session
// gate (ADR 0024 decision 10): it checks the session itself and hands back a
// connection that stops working when the session does. Connect returns the
// connection and the instance's `hermes serve` token.
type Connector interface {
	Connect(ctx context.Context, account string) (net.Conn, string, error)
}

var (
	// ErrNoAssistant reports an account without an assistant.
	ErrNoAssistant = errors.New("this account has no assistant")
	// ErrSessionRejected reports that the gate did not accept the session.
	ErrSessionRejected = errors.New("the session was not accepted for the assistant")
)

// SocketConnector dials the member's socket directly with a token from
// TokenDir/<account>.token. It is for development only, where the Product
// Service runs without the File Broker; it is no session gate.
type SocketConnector struct {
	SocketDir string
	TokenDir  string
}

func (s SocketConnector) Connect(ctx context.Context, account string) (net.Conn, string, error) {
	token, err := os.ReadFile(filepath.Join(s.TokenDir, account+".token"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNoAssistant
	}
	if err != nil {
		return nil, "", err
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", filepath.Join(s.SocketDir, account+".sock"))
	if errors.Is(err, syscall.ENOENT) {
		return nil, "", ErrNoAssistant
	}
	if err != nil {
		return nil, "", err
	}
	return conn, strings.TrimSpace(string(token)), nil
}

// instanceHost is where `hermes serve` listens inside the sandbox; it refuses
// a Host that is not a loopback address.
const instanceHost = "127.0.0.1:9119"

// tokenHeader carries the instance's token on REST requests.
const tokenHeader = "X-Hermes-Session-Token"

// gated is an HTTP client whose one connection comes through the gate.
func gated(conn net.Conn) *http.Client {
	used := false
	return &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			if used {
				return nil, errors.New("assistant connection already used")
			}
			used = true
			return conn, nil
		},
		DisableKeepAlives: true,
		// Hermes answers once the instance is up; a cold start takes seconds.
		ResponseHeaderTimeout: requestTimeout,
	}}
}

// rest makes one REST request to the account's instance and returns the
// status and the body. Each request gets its own gated connection.
func (h *handler) rest(ctx context.Context, account, method, path string, body []byte) (int, []byte, error) {
	conn, token, err := h.options.Connector.Connect(ctx, account)
	if err != nil {
		return 0, nil, err
	}
	defer conn.Close()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://"+instanceHost+path, reader)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set(tokenHeader, token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := gated(conn).Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamBytes))
	return response.StatusCode, data, err
}

// dialGateway opens the instance's JSON-RPC WebSocket through the gate. The
// URL carries the token, so it is never logged.
func (h *handler) dialGateway(ctx context.Context, account string) (*websocket.Conn, error) {
	conn, token, err := h.options.Connector.Connect(ctx, account)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	upstream, response, err := websocket.Dial(dialCtx, "ws://"+instanceHost+"/api/ws?token="+token, &websocket.DialOptions{HTTPClient: gated(conn)})
	if err != nil {
		_ = conn.Close()
		if response != nil {
			return nil, fmt.Errorf("instance refused the gateway: %d", response.StatusCode)
		}
		// Not err itself: it would name the URL and its token.
		return nil, errors.New("instance gateway unreachable")
	}
	upstream.SetReadLimit(maxUpstreamBytes)
	return upstream, nil
}
