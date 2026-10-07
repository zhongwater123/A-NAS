// Package sessionlookup lets the photo service ask the root Host Agent who
// stands behind a session token (ADR 0011). The Host Agent reads the session
// table read-only; the photo service never opens control.db, which also holds
// password digests, and never trusts the Product Service's claim about the
// user.
package sessionlookup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
)

const ResolvePath = "/v1/sessions/resolve"

type Resolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
}

type resolveRequest struct {
	Token string `json:"token"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// NewHandler serves ResolvePath for one resolver.
func NewHandler(resolver Resolver, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+ResolvePath, func(w http.ResponseWriter, r *http.Request) {
		var request resolveRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_request"})
			return
		}
		user, err := resolver.ResolveSessionUser(r.Context(), request.Token)
		switch {
		case errors.Is(err, accounts.ErrSessionNotFound):
			writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "session_invalid"})
		case err != nil:
			logger.ErrorContext(r.Context(), "session lookup failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "sessions_unavailable"})
		default:
			writeJSON(w, http.StatusOK, user)
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// Client resolves sessions through the Host Agent's lookup socket.
type Client struct {
	client *http.Client
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
	}
	return &Client{client: &http.Client{Transport: transport, Timeout: 5 * time.Second}}
}

// ResolveSessionUser returns accounts.ErrSessionNotFound for tokens that do
// not name an active, unexpired session.
func (c *Client) ResolveSessionUser(ctx context.Context, token string) (accounts.SessionUser, error) {
	body, err := json.Marshal(resolveRequest{Token: token})
	if err != nil {
		return accounts.SessionUser{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://host-agent"+ResolvePath, bytes.NewReader(body))
	if err != nil {
		return accounts.SessionUser{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return accounts.SessionUser{}, fmt.Errorf("reach the session lookup: %w", err)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
		var user accounts.SessionUser
		if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&user); err != nil || user.UserID == "" {
			return accounts.SessionUser{}, errors.New("session lookup returned an invalid user")
		}
		return user, nil
	case http.StatusUnauthorized:
		return accounts.SessionUser{}, accounts.ErrSessionNotFound
	default:
		return accounts.SessionUser{}, fmt.Errorf("session lookup answered %s", response.Status)
	}
}
