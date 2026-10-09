//go:build unix

package terminal_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/zhongwater123/A-NAS/internal/terminal"
)

func TestStatusReportsConfiguration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		recorder := httptest.NewRecorder()
		terminal.New(terminal.Config{Enabled: enabled}, nil).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, terminal.StatusPath, nil))

		var body struct{ Enabled bool }
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if recorder.Code != http.StatusOK || body.Enabled != enabled {
			t.Fatalf("status = %d enabled = %v, want 200 and %v", recorder.Code, body.Enabled, enabled)
		}
	}
}

func TestSessionRequestsAreRejectedBeforeUpgrade(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		remoteAddr string
		host       string
		upgrade    bool
		wantStatus int
		wantCode   string
	}{
		{name: "disabled", enabled: false, remoteAddr: "127.0.0.1:40000", host: "127.0.0.1:8080", upgrade: true, wantStatus: http.StatusForbidden, wantCode: "terminal_disabled"},
		{name: "remote peer", enabled: true, remoteAddr: "192.0.2.10:40000", host: "127.0.0.1:8080", upgrade: true, wantStatus: http.StatusForbidden, wantCode: "terminal_forbidden"},
		{name: "rebound host", enabled: true, remoteAddr: "127.0.0.1:40000", host: "attacker.example:8080", upgrade: true, wantStatus: http.StatusForbidden, wantCode: "terminal_forbidden"},
		{name: "plain request", enabled: true, remoteAddr: "[::1]:40000", host: "localhost:8080", upgrade: false, wantStatus: http.StatusUpgradeRequired, wantCode: "upgrade_required"},
		{name: "LAN browser through the entry", enabled: true, remoteAddr: "127.0.0.1:40000", host: "172.18.45.48", upgrade: false, wantStatus: http.StatusUpgradeRequired, wantCode: "upgrade_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, terminal.SessionPath, nil)
			request.RemoteAddr = test.remoteAddr
			request.Host = test.host
			if test.upgrade {
				request.Header.Set("Connection", "Upgrade")
				request.Header.Set("Upgrade", "websocket")
			}
			recorder := httptest.NewRecorder()
			terminal.New(terminal.Config{Enabled: test.enabled, Shell: "/bin/sh"}, nil).ServeHTTP(recorder, request)

			var body struct{ Error struct{ Code string } }
			_ = json.Unmarshal(recorder.Body.Bytes(), &body)
			if recorder.Code != test.wantStatus || body.Error.Code != test.wantCode {
				t.Fatalf("got %d %q, want %d %q", recorder.Code, body.Error.Code, test.wantStatus, test.wantCode)
			}
		})
	}
}

func TestSessionRejectsCrossOriginBrowser(t *testing.T) {
	server := httptest.NewServer(terminal.New(terminal.Config{Enabled: true, Shell: "/bin/sh"}, nil))
	defer server.Close()

	_, response, err := websocket.Dial(context.Background(), wsURL(server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {"http://attacker.example"}},
	})
	if err == nil || response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin dial err = %v response = %v, want 403", err, response)
	}
}

func TestSessionRunsShellUntilExit(t *testing.T) {
	server := httptest.NewServer(terminal.New(terminal.Config{Enabled: true, Shell: "/bin/sh"}, nil))
	defer server.Close()
	conn := dial(t, server)

	send(t, conn, "echo anas-$((40 + 2))\n")
	readUntil(t, conn, "anas-42")
	send(t, conn, "exit 3\n")

	status, reason := readClose(t, conn)
	if status != websocket.StatusNormalClosure || reason != "exit 3" {
		t.Fatalf("close = %v %q, want normal closure with exit 3", status, reason)
	}
}

func TestSessionAppliesResize(t *testing.T) {
	server := httptest.NewServer(terminal.New(terminal.Config{Enabled: true, Shell: "/bin/sh"}, nil))
	defer server.Close()
	conn := dial(t, server)

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"resize","cols":123,"rows":45}`)); err != nil {
		t.Fatalf("send resize: %v", err)
	}
	send(t, conn, "stty size\n")
	readUntil(t, conn, "45 123")
}

func TestSessionSurvivesServerTimeouts(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:      terminal.New(terminal.Config{Enabled: true, Shell: "/bin/sh"}, nil),
		ReadTimeout:  200 * time.Millisecond,
		WriteTimeout: 200 * time.Millisecond,
	}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	conn, _, err := websocket.Dial(context.Background(), "ws://"+listener.Addr().String()+terminal.SessionPath, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	time.Sleep(500 * time.Millisecond)
	send(t, conn, "echo still-$((1 + 1))\n")
	readUntil(t, conn, "still-2")
}

func TestSessionLimitIsEnforced(t *testing.T) {
	server := httptest.NewServer(terminal.New(terminal.Config{Enabled: true, Shell: "/bin/sh", MaxSessions: 1}, nil))
	defer server.Close()
	first := dial(t, server)
	send(t, first, "echo first-ready\n")
	readUntil(t, first, "first-ready")

	_, response, err := websocket.Dial(context.Background(), wsURL(server), nil)
	if err == nil || response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second dial err = %v response = %v, want 429", err, response)
	}
}

func TestShutdownEndsSessions(t *testing.T) {
	handler := terminal.New(terminal.Config{Enabled: true, Shell: "/bin/sh"}, nil)
	server := httptest.NewServer(handler)
	defer server.Close()
	conn := dial(t, server)
	send(t, conn, "echo ready\n")
	readUntil(t, conn, "ready")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdown := make(chan error, 1)
	go func() { shutdown <- handler.Shutdown(ctx) }()
	if status, _ := readClose(t, conn); status != websocket.StatusGoingAway {
		t.Fatalf("close status = %v, want going away", status)
	}
	if err := <-shutdown; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if _, response, err := websocket.Dial(context.Background(), wsURL(server), nil); err == nil || response == nil || response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after shutdown err = %v, want refusal", err)
	}
}

func wsURL(server *httptest.Server) string {
	return "ws" + strings.TrimPrefix(server.URL, "http") + terminal.SessionPath
}

func dial(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(context.Background(), wsURL(server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {server.URL}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func send(t *testing.T, conn *websocket.Conn, input string) {
	t.Helper()
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte(input)); err != nil {
		t.Fatalf("send input: %v", err)
	}
}

func readUntil(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var output strings.Builder
	for !strings.Contains(output.String(), want) {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for %q: %v; output so far %q", want, err, output.String())
		}
		output.Write(data)
	}
}

func readClose(t *testing.T, conn *websocket.Conn) (websocket.StatusCode, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, _, err := conn.Read(ctx)
		if err == nil {
			continue
		}
		var closeError websocket.CloseError
		if !errors.As(err, &closeError) {
			t.Fatalf("expected close frame, got %v", err)
		}
		return closeError.Code, closeError.Reason
	}
}
