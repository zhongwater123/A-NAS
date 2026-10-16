// Package sessionlookup lets the photo service ask the root Host Agent who
// stands behind a session token (ADR 0011), or behind the Linux UID a member's
// AI assistant runs as (ADR 0024). The Host Agent reads the session
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

const (
	ResolvePath = "/v1/sessions/resolve"
	// AccountUIDPath names the active user behind a Linux UID, for a member's
	// AI assistant, which runs as the member's own UID and has no session.
	AccountUIDPath = "/v1/accounts/resolve-uid"
)

type Resolver interface {
	ResolveSessionUser(context.Context, string) (accounts.SessionUser, error)
}

// AccountResolver is the optional lookup behind AccountUIDPath.
type AccountResolver interface {
	ResolveAccountUID(context.Context, int) (accounts.SessionUser, error)
}

type resolveRequest struct {
	Token string `json:"token"`
}

type accountRequest struct {
	UID int `json:"uid"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// NewHandler serves ResolvePath for one resolver, and AccountUIDPath only when
// accountResolver is set: the lookup by UID names any account to whoever asks,
// so it is on only while the photo service serves assistants.
func NewHandler(resolver Resolver, accountResolver AccountResolver, logger *slog.Logger) http.Handler {
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
	if accountResolver != nil {
		mux.HandleFunc("POST "+AccountUIDPath, func(w http.ResponseWriter, r *http.Request) {
			var request accountRequest
			decoder := json.NewDecoder(io.LimitReader(r.Body, 4<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&request); err != nil || request.UID <= 0 {
				writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid_request"})
				return
			}
			user, err := accountResolver.ResolveAccountUID(r.Context(), request.UID)
			switch {
			case errors.Is(err, accounts.ErrUserNotFound):
				writeJSON(w, http.StatusUnauthorized, errorResponse{Error: "account_invalid"})
			case err != nil:
				logger.ErrorContext(r.Context(), "account lookup failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "accounts_unavailable"})
			default:
				writeJSON(w, http.StatusOK, user)
			}
		})
	}
	if directory, ok := resolver.(interface {
		ActiveProfiles(context.Context) ([]accounts.Profile, error)
	}); ok {
		mux.HandleFunc("GET /v1/members", func(w http.ResponseWriter, r *http.Request) {
			members, err := directory.ActiveProfiles(r.Context())
			if err != nil {
				writeJSON(w, 503, errorResponse{Error: "members_unavailable"})
				return
			}
			writeJSON(w, 200, members)
		})
	}
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
	return c.lookup(ctx, ResolvePath, resolveRequest{Token: token}, http.StatusUnauthorized, accounts.ErrSessionNotFound)
}

// ResolveAccountUID returns accounts.ErrUserNotFound for a UID that belongs
// to no active user.
func (c *Client) ResolveAccountUID(ctx context.Context, uid int) (accounts.SessionUser, error) {
	return c.lookup(ctx, AccountUIDPath, accountRequest{UID: uid}, http.StatusUnauthorized, accounts.ErrUserNotFound)
}

func (c *Client) lookup(ctx context.Context, path string, query any, missingStatus int, missing error) (accounts.SessionUser, error) {
	body, err := json.Marshal(query)
	if err != nil {
		return accounts.SessionUser{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://host-agent"+path, bytes.NewReader(body))
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
	case missingStatus:
		return accounts.SessionUser{}, missing
	default:
		return accounts.SessionUser{}, fmt.Errorf("session lookup answered %s", response.Status)
	}
}

// ActiveProfiles lists enabled recipients through the private lookup socket.
func (c *Client) ActiveProfiles(ctx context.Context) ([]accounts.Profile, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://host-agent/v1/members", nil)
	if err != nil {
		return nil, err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("member directory answered %s", response.Status)
	}
	var profiles []accounts.Profile
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&profiles); err != nil {
		return nil, err
	}
	return profiles, nil
}
