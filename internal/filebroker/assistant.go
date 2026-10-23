package filebroker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/assistantlogin"
)

const opAssistant = "assistant"

// assistantAccount is the account name form the assistant's units accept; it
// names the socket and token files.
var assistantAccount = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// serveAssistant is the session gate in front of the members' AI assistants
// (ADR 0024 decision 10). Only root may connect to an assistant's socket; the
// broker does so for the member behind a valid session and hands the
// connection back with an access token of Hermes's own login to the instance,
// keeping its own copy of the connection. The login's password, the
// instance's secret, never leaves root. Like a terminal, the session is
// checked again while the connection is open, and when it stops being valid
// the connection is cut for every holder.
func (s *Server) serveAssistant(conn *net.UnixConn, req request) {
	ctx := context.Background()
	identity, refused := s.identify(ctx, req.Token)
	if refused != nil {
		_ = writeMessage(conn, response{Error: refused}, nil)
		return
	}
	if s.config.AssistantSockets == "" {
		_ = writeMessage(conn, response{Error: &wireError{Code: codeUnavailable, Message: "the assistant is not enabled"}}, nil)
		return
	}
	if !assistantAccount.MatchString(identity.Username) {
		_ = writeMessage(conn, response{Error: &wireError{Code: codeForbidden, Message: "this account has no assistant"}}, nil)
		return
	}
	instance, token, err := s.dialAssistant(ctx, identity.Username, req.Fresh)
	if err != nil {
		code := codeUnavailable
		if errors.Is(err, fs.ErrNotExist) {
			code = codeNotFound
		} else {
			s.config.Logger.Error("assistant unreachable", "username", identity.Username, "error", err)
		}
		_ = writeMessage(conn, response{Error: &wireError{Code: code, Message: "this account's assistant is not available"}}, nil)
		return
	}
	defer instance.Close()
	if !s.hold(instance) {
		return
	}
	defer s.release(instance)
	file, err := instance.File()
	if err != nil {
		_ = writeMessage(conn, response{Error: &wireError{Code: codeUnavailable}}, nil)
		return
	}
	err = writeMessage(conn, response{Assistant: &assistantGrant{Username: identity.Username, Token: token}}, file)
	_ = file.Close()
	if err != nil {
		cut(instance)
		return
	}
	closed := make(chan struct{})
	go func() {
		// The client sends nothing more; a read returns when it is done.
		_, _ = conn.Read(make([]byte, 1))
		close(closed)
	}()
	recheck := time.NewTicker(s.config.TerminalRecheck)
	defer recheck.Stop()
	unconfirmed := 0
	for {
		select {
		case <-closed:
			return
		case <-recheck.C:
			if !s.recheck(ctx, req.Token, &unconfirmed, func(current accounts.Identity) bool { return current.UID == identity.UID }) {
				s.config.Logger.Info("assistant session no longer valid; cutting the connection", "username", identity.Username)
				cut(instance)
				_ = writeMessage(conn, response{Error: &wireError{Code: codeUnauthenticated}}, nil)
				return
			}
		}
	}
}

// assistantLoginTimeout covers a cold start of the instance, which the first
// login triggers through its socket.
const assistantLoginTimeout = 90 * time.Second

func (s *Server) dialAssistant(ctx context.Context, username string, fresh bool) (*net.UnixConn, string, error) {
	secret, err := readAssistantToken(filepath.Join(s.config.AssistantTokens, username+".env"))
	if err != nil {
		return nil, "", err
	}
	token, err := s.logins.Token(ctx, username, fresh, func(ctx context.Context) (string, time.Time, error) {
		ctx, cancel := context.WithTimeout(ctx, assistantLoginTimeout)
		defer cancel()
		connection, err := s.connectAssistant(username)
		if err != nil {
			return "", time.Time{}, err
		}
		defer connection.Close()
		return assistantlogin.Login(ctx, connection, username, secret)
	})
	if err != nil {
		return nil, "", err
	}
	connection, err := s.connectAssistant(username)
	if err != nil {
		return nil, "", err
	}
	return connection, token, nil
}

func (s *Server) connectAssistant(username string) (*net.UnixConn, error) {
	dialer := net.Dialer{Timeout: 10 * time.Second}
	connection, err := dialer.Dial("unix", filepath.Join(s.config.AssistantSockets, username+".sock"))
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w: %v", fs.ErrNotExist, err)
		}
		return nil, err
	}
	return connection.(*net.UnixConn), nil
}

// readAssistantToken reads the instance's secret, HERMES_DASHBOARD_SESSION_TOKEN
// in the root-only token file: the member's password for Hermes's login.
func readAssistantToken(path string) (string, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	lines := bufio.NewScanner(file)
	for lines.Scan() {
		if value, ok := strings.CutPrefix(lines.Text(), "HERMES_DASHBOARD_SESSION_TOKEN="); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("no assistant secret in %s", path)
}

func (s *Server) hold(instance *net.UnixConn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.assistants[instance] = struct{}{}
	return true
}

func (s *Server) release(instance *net.UnixConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.assistants, instance)
}

// cut shuts the connection down, which every descriptor of it observes,
// unlike closing the broker's own copy.
func cut(instance *net.UnixConn) {
	_ = instance.CloseRead()
	_ = instance.CloseWrite()
}

// AssistantConn is a connection to the signed-in member's assistant, handed
// out by the File Broker. It stops working when the member's session does.
type AssistantConn struct {
	net.Conn
	// Username is the member the broker found behind the session.
	Username string
	// Token is an access token of Hermes's own login to the instance.
	Token   string
	control *net.UnixConn
	close   sync.Once
}

// ConnectAssistant asks the File Broker for a connection to the assistant of
// the member signed in on the context's session. fresh asks for a new login,
// when the instance refused the last token.
func (c *Client) ConnectAssistant(ctx context.Context, fresh bool) (*AssistantConn, error) {
	token := accounts.SessionToken(ctx)
	if token == "" {
		return nil, fmt.Errorf("%w: no session for the assistant", ErrUnavailable)
	}
	var dialer net.Dialer
	connection, err := dialer.DialContext(ctx, "unix", c.socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	control := connection.(*net.UnixConn)
	stop := context.AfterFunc(ctx, func() { _ = control.Close() })
	defer stop()
	if err := writeMessage(control, request{Token: token, Op: opAssistant, Fresh: fresh}, nil); err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var result response
	file, err := readMessage(control, &result)
	if err == nil && result.Error != nil {
		err = result.Error
	}
	if err == nil && (file == nil || result.Assistant == nil) {
		err = fmt.Errorf("%w: no assistant connection returned", ErrUnavailable)
	}
	if err != nil {
		closeFile(file)
		_ = control.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	instance, err := net.FileConn(file)
	closeFile(file)
	if err != nil {
		_ = control.Close()
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	assistant := &AssistantConn{Conn: instance, Username: result.Assistant.Username, Token: result.Assistant.Token, control: control}
	go assistant.watch()
	return assistant, nil
}

// watch closes the connection when the broker ends it or goes away. The
// broker cuts the connection itself; this only releases it promptly.
func (a *AssistantConn) watch() {
	var result response
	file, _ := readMessage(a.control, &result)
	closeFile(file)
	_ = a.Close()
}

// Close closes the connection and lets the broker forget it.
func (a *AssistantConn) Close() error {
	var err error
	a.close.Do(func() {
		err = a.Conn.Close()
		_ = a.control.Close()
	})
	return err
}
