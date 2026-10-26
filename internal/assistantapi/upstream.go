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
	"time"

	"github.com/coder/websocket"

	"github.com/zhongwater123/A-NAS/internal/assistantlogin"
)

// Connector reaches the instance of the member signed in on the context's
// session (accounts.SessionToken). In A-NAS the File Broker is the session
// gate (ADR 0024 decision 10): it checks the session itself and hands back a
// connection that stops working when the session does. Connect returns the
// connection and an access token of Hermes's own login to the instance;
// fresh asks for a new login, when the instance refused the last token.
type Connector interface {
	Connect(ctx context.Context, account string, fresh bool) (net.Conn, string, error)
}

var (
	// ErrNoAssistant reports an account without an assistant.
	ErrNoAssistant = errors.New("this account has no assistant")
	// ErrSessionRejected reports that the gate did not accept the session.
	ErrSessionRejected = errors.New("the session was not accepted for the assistant")
)

// SocketConnector dials the member's socket directly and logs in with the
// instance's secret from TokenDir/<account>.token, as the File Broker does.
// It is for development only, where the Product Service runs without the File
// Broker; it is no session gate.
type SocketConnector struct {
	SocketDir string
	TokenDir  string
	logins    *assistantlogin.Cache
}

// NewSocketConnector returns a SocketConnector with its own login cache.
func NewSocketConnector(socketDir, tokenDir string) SocketConnector {
	return SocketConnector{SocketDir: socketDir, TokenDir: tokenDir, logins: &assistantlogin.Cache{}}
}

func (s SocketConnector) Connect(ctx context.Context, account string, fresh bool) (net.Conn, string, error) {
	secret, err := os.ReadFile(filepath.Join(s.TokenDir, account+".token"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNoAssistant
	}
	if err != nil {
		return nil, "", err
	}
	logins := s.logins
	if logins == nil {
		logins = &assistantlogin.Cache{}
	}
	token, err := logins.Token(ctx, account, fresh, func(ctx context.Context) (string, time.Time, error) {
		conn, err := s.dial(ctx, account)
		if err != nil {
			return "", time.Time{}, err
		}
		defer conn.Close()
		return assistantlogin.Login(ctx, conn, account, strings.TrimSpace(string(secret)))
	})
	if err != nil {
		return nil, "", err
	}
	conn, err := s.dial(ctx, account)
	if err != nil {
		return nil, "", err
	}
	return conn, token, nil
}

func (s SocketConnector) dial(ctx context.Context, account string) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", filepath.Join(s.SocketDir, account+".sock"))
	if errors.Is(err, syscall.ENOENT) {
		return nil, ErrNoAssistant
	}
	return conn, err
}

// instanceHost is where `hermes serve` listens inside the sandbox; it refuses
// a Host that is not a loopback address.
const instanceHost = assistantlogin.Host

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
// status and the body. Each request gets its own gated connection. A refused
// access token (the instance restarted) is replaced by a fresh login once.
func (h *Handler) rest(ctx context.Context, account, method, path string, body []byte) (int, []byte, error) {
	status, data, err := h.restOnce(ctx, account, method, path, body, false)
	if err == nil && status == http.StatusUnauthorized {
		return h.restOnce(ctx, account, method, path, body, true)
	}
	return status, data, err
}

func (h *Handler) restOnce(ctx context.Context, account, method, path string, body []byte, fresh bool) (int, []byte, error) {
	conn, token, err := h.options.Connector.Connect(ctx, account, fresh)
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
	request.Header.Set("Authorization", "Bearer "+token)
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
// URL carries the access token, so it is never logged. A refused token (the
// instance restarted) is replaced by a fresh login once.
func (h *Handler) dialGateway(ctx context.Context, account string) (*websocket.Conn, error) {
	upstream, refused, err := h.dialGatewayOnce(ctx, account, false)
	if refused {
		upstream, _, err = h.dialGatewayOnce(ctx, account, true)
	}
	return upstream, err
}

func (h *Handler) dialGatewayOnce(ctx context.Context, account string, fresh bool) (*websocket.Conn, bool, error) {
	conn, token, err := h.options.Connector.Connect(ctx, account, fresh)
	if err != nil {
		return nil, false, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	upstream, response, err := websocket.Dial(dialCtx, "ws://"+instanceHost+"/api/ws?token="+token, &websocket.DialOptions{HTTPClient: gated(conn)})
	if err != nil {
		_ = conn.Close()
		if response != nil {
			refused := response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden
			return nil, refused, fmt.Errorf("instance refused the gateway: %d", response.StatusCode)
		}
		// Not err itself: it would name the URL and its token.
		return nil, false, errors.New("instance gateway unreachable")
	}
	upstream.SetReadLimit(maxUpstreamBytes)
	return upstream, false, nil
}
