// Package accountremoval lets the Product Service ask the photo, share and
// family services to remove what they keep for an account whose deletion has
// started (docs/specs/account-enable-and-delete.md). The request only names
// the account. Each service then asks the Host Agent, which reads the control
// plane itself, whether that deletion has started and what the administrator
// chose: the services never act on the Product Service's word (ADR 0011).
package accountremoval

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// Path is where a service takes the request, followed by the user ID. The
// Product Service forwards only /api/ paths to these services, so no browser
// reaches it.
const Path = "/internal/v1/account-removals/"

// Lookup asks the Host Agent about an account's deletion.
type Lookup func(context.Context, string) (accountid.AccountDeletion, error)

// Remove removes what a service keeps for the account; what is already gone
// counts as removed.
type Remove func(context.Context, accountid.AccountDeletion) error

// Route answers Path with removal and everything else with next.
func Route(lookup Lookup, remove Remove, logger *slog.Logger, next http.Handler) http.Handler {
	removal := Handler(lookup, remove, logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, Path) {
			removal.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Handler serves POST Path{userID}: 204 once removed, 409 not_deleting for an
// account whose deletion has not started.
func Handler(lookup Lookup, remove Remove, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		userID := strings.TrimPrefix(r.URL.Path, Path)
		if userID == "" || strings.Contains(userID, "/") {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "user ID is invalid")
			return
		}
		deletion, err := lookup(r.Context(), userID)
		if errors.Is(err, accountid.ErrNotDeleting) {
			httpjson.WriteError(w, http.StatusConflict, "not_deleting", "the account is not being deleted")
			return
		}
		if err == nil {
			err = remove(r.Context(), deletion)
		}
		if err != nil {
			logger.ErrorContext(r.Context(), "account removal failed", "user_id", userID, "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "removal_failed", "the account's data could not be removed")
			return
		}
		logger.InfoContext(r.Context(), "account data removed", "user_id", userID)
		w.WriteHeader(http.StatusNoContent)
	})
}

// Request asks the service on socketPath to remove what it keeps for userID.
func Request(ctx context.Context, socketPath, userID string) error {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://service"+Path+url.PathEscape(userID), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("reach %s: %w", socketPath, err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	return fmt.Errorf("%s answered %s: %s", socketPath, response.Status, strings.TrimSpace(string(detail)))
}
