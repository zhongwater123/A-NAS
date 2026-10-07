// Package agent carries App Center operations between the Product Service
// and the container agent over the agent's Unix socket. Installs name a
// catalog app and the digest of a confirmed plan; there is no way to submit a
// Compose file.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

const requestTimeout = 20 * time.Second

// Client implements appstore.Store by calling the container agent.
type Client struct {
	httpClient *http.Client
}

func NewClient(socketPath string) *Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &Client{httpClient: &http.Client{Transport: transport, Timeout: requestTimeout}}
}

func (c *Client) Apps(ctx context.Context) ([]appstore.AppStatus, error) {
	var document AppsDocument
	if _, err := c.do(ctx, http.MethodGet, "/v1/apps", nil, &document); err != nil {
		return nil, err
	}
	return document.ToDomain()
}

func (c *Client) Icon(ctx context.Context, id string) ([]byte, string, error) {
	if !appstore.ValidID(id) {
		return nil, "", appstore.ErrNotFound
	}
	var icon []byte
	contentType, err := c.do(ctx, http.MethodGet, "/v1/apps/"+id+"/icon", nil, &icon)
	return icon, contentType, err
}

func (c *Client) Plan(ctx context.Context, id string, identity appstore.Identity) (appstore.Plan, error) {
	if !appstore.ValidID(id) {
		return appstore.Plan{}, appstore.ErrNotFound
	}
	var document PlanDocument
	path := fmt.Sprintf("/v1/apps/%s/plan?uid=%d&gid=%d", id, identity.UID, identity.GID)
	if _, err := c.do(ctx, http.MethodGet, path, nil, &document); err != nil {
		return appstore.Plan{}, err
	}
	return document.ToDomain(), nil
}

func (c *Client) Install(ctx context.Context, id, digest string, identity appstore.Identity) (appstore.Job, error) {
	if !appstore.ValidID(id) {
		return appstore.Job{}, appstore.ErrNotFound
	}
	var document JobDocument
	request := InstallRequest{Digest: digest, UID: identity.UID, GID: identity.GID}
	if _, err := c.do(ctx, http.MethodPost, "/v1/apps/"+id+"/install", request, &document); err != nil {
		return appstore.Job{}, err
	}
	return document.ToDomain()
}

func (c *Client) Uninstall(ctx context.Context, id string) (appstore.Job, error) {
	if !appstore.ValidID(id) {
		return appstore.Job{}, appstore.ErrNotFound
	}
	var document JobDocument
	if _, err := c.do(ctx, http.MethodPost, "/v1/apps/"+id+"/uninstall", struct{}{}, &document); err != nil {
		return appstore.Job{}, err
	}
	return document.ToDomain()
}

func (c *Client) do(ctx context.Context, method, path string, body any, into any) (string, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		reader = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://container-agent"+path, reader)
	if err != nil {
		return "", err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 4<<20)
	if response.StatusCode >= 300 {
		var failure errorDocument
		_ = json.NewDecoder(limited).Decode(&failure)
		return "", ErrorFromCode(failure.Error.Code, failure.Error.Message, response.StatusCode)
	}
	if raw, ok := into.(*[]byte); ok {
		*raw, err = io.ReadAll(limited)
		return response.Header.Get("Content-Type"), err
	}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return "", fmt.Errorf("decode container agent response: %w", err)
	}
	return response.Header.Get("Content-Type"), nil
}

type handler struct {
	store  appstore.Store
	logger *slog.Logger
}

// NewHandler serves /v1/apps on the container agent socket.
func NewHandler(store appstore.Store, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{store: store, logger: logger}
}

// Matches reports whether a path belongs to the App Center protocol.
func Matches(path string) bool {
	return path == "/v1/apps" || strings.HasPrefix(path, "/v1/apps/")
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/apps" {
		if !allow(w, r, http.MethodGet) {
			return
		}
		apps, err := h.store.Apps(r.Context())
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, AppsFromDomain(apps))
		return
	}
	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/apps/"), "/")
	if !ok || !appstore.ValidID(id) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch operation {
	case "icon":
		if !allow(w, r, http.MethodGet) {
			return
		}
		icon, contentType, err := h.store.Icon(r.Context(), id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(icon)
	case "plan":
		if !allow(w, r, http.MethodGet) {
			return
		}
		identity, ok := identityFrom(id, r.URL.Query().Get("uid"), r.URL.Query().Get("gid"))
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "app identity is invalid")
			return
		}
		plan, err := h.store.Plan(r.Context(), id, identity)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, PlanFromDomain(plan))
	case "install", "uninstall":
		if !allow(w, r, http.MethodPost) {
			return
		}
		var request InstallRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
			return
		}
		var job appstore.Job
		var err error
		if operation == "install" {
			identity, ok := identityFrom(id, strconv.Itoa(request.UID), strconv.Itoa(request.GID))
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_request", "app identity is invalid")
				return
			}
			job, err = h.store.Install(r.Context(), id, request.Digest, identity)
		} else {
			job, err = h.store.Uninstall(r.Context(), id)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.logger.InfoContext(r.Context(), "app job started", "app", id, "action", job.Action)
		writeJSON(w, http.StatusAccepted, JobFromDomain(job))
	default:
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

// identityFrom accepts only the app identity range the Host Agent
// allocates, so a request can never render a plan that runs as root or as a
// member's account.
func identityFrom(id, uid, gid string) (appstore.Identity, bool) {
	parsedUID, uidErr := strconv.Atoi(uid)
	parsedGID, gidErr := strconv.Atoi(gid)
	if uidErr != nil || gidErr != nil || parsedUID != parsedGID ||
		parsedUID < appstore.FirstAppUID || parsedUID > appstore.LastAppUID {
		return appstore.Identity{}, false
	}
	return appstore.Identity{Username: appstore.IdentityName(id), UID: parsedUID, GID: parsedGID}, true
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	status, code := StatusForError(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "app store operation failed", "path", r.URL.Path, "error", err)
	}
	writeError(w, status, code, err.Error())
}

var errorCodes = []struct {
	err    error
	status int
	code   string
}{
	{appstore.ErrNotFound, http.StatusNotFound, "app_not_found"},
	{appstore.ErrPlanChanged, http.StatusConflict, "plan_changed"},
	{appstore.ErrBusy, http.StatusConflict, "app_busy"},
	{appstore.ErrAlreadyInstalled, http.StatusConflict, "already_installed"},
	{appstore.ErrNotInstalled, http.StatusConflict, "not_installed"},
	{appstore.ErrPortInUse, http.StatusConflict, "port_in_use"},
	{appstore.ErrNameInUse, http.StatusConflict, "name_in_use"},
	{appstore.ErrUnavailable, http.StatusServiceUnavailable, "apps_unavailable"},
}

// StatusForError maps domain errors to codes shared with the product API.
func StatusForError(err error) (int, string) {
	for _, mapping := range errorCodes {
		if errors.Is(err, mapping.err) {
			return mapping.status, mapping.code
		}
	}
	var policy *appstore.PolicyError
	if errors.As(err, &policy) {
		return http.StatusUnprocessableEntity, "policy_violation"
	}
	return http.StatusBadGateway, "app_engine_error"
}

// ErrorFromCode restores the domain error for a code, keeping the agent's
// message for conflicts such as which container holds a port.
func ErrorFromCode(code, message string, status int) error {
	for _, mapping := range errorCodes {
		if mapping.code == code {
			if message != "" && message != mapping.err.Error() {
				return fmt.Errorf("%w: %s", mapping.err, strings.TrimPrefix(message, mapping.err.Error()+": "))
			}
			return mapping.err
		}
	}
	if code == "policy_violation" {
		return &appstore.PolicyError{Reasons: []string{message}}
	}
	return fmt.Errorf("container agent returned status %d (%s)", status, code)
}

func allow(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorDocument struct {
	Error errorDetail `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorDocument{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(err)
	}
}

var _ appstore.Store = (*Client)(nil)
