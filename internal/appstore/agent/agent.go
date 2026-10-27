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
	"os/user"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
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

// Plan asks for the app's plan. The agent renders it as the app's account;
// identity, the one the Host Agent reserved, is shown until the account
// exists. Agents before issue #176 render it as identity.
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

// Install starts installing the confirmed plan as the app's account, which
// the agent looks up itself; identity is sent for agents before issue #176,
// which require it.
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

func (c *Client) ReserveInstall(ctx context.Context, id, digest string, identity appstore.Identity) (appstore.InstallReservation, error) {
	if !appstore.ValidID(id) {
		return appstore.InstallReservation{}, appstore.ErrNotFound
	}
	var document ReservationDocument
	request := InstallRequest{Digest: digest, UID: identity.UID, GID: identity.GID}
	if _, err := c.do(ctx, http.MethodPost, "/v1/apps/"+id+"/install-reserve", request, &document); err != nil {
		return appstore.InstallReservation{}, err
	}
	job, err := document.Job.ToDomain()
	if err != nil {
		return appstore.InstallReservation{}, err
	}
	if document.Token == "" {
		return appstore.InstallReservation{}, fmt.Errorf("%w: missing install reservation", appstore.ErrUnavailable)
	}
	return appstore.InstallReservation{Token: document.Token, Job: job}, nil
}

// The agent verifies the host account at commit; the client's identity is
// deliberately not sent, so it cannot override the actual Linux account.
func (c *Client) CommitInstall(ctx context.Context, id, token string, _ appstore.Identity) (appstore.Job, error) {
	if !appstore.ValidID(id) {
		return appstore.Job{}, appstore.ErrNotFound
	}
	var document JobDocument
	if _, err := c.do(ctx, http.MethodPost, "/v1/apps/"+id+"/install-commit", PreparationRequest{Token: token}, &document); err != nil {
		return appstore.Job{}, err
	}
	return document.ToDomain()
}

func (c *Client) CancelInstall(ctx context.Context, id, token string) error {
	if !appstore.ValidID(id) {
		return appstore.ErrNotFound
	}
	var response struct{}
	_, err := c.do(ctx, http.MethodPost, "/v1/apps/"+id+"/install-cancel", PreparationRequest{Token: token}, &response)
	return err
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
	// The agent is installed apart from the product service, so a newer agent
	// may add fields; an older client ignores them rather than failing every
	// request (the 2026-10-09 App Center outage).
	if err := json.NewDecoder(limited).Decode(into); err != nil {
		return "", fmt.Errorf("decode container agent response: %w", err)
	}
	return response.Header.Get("Content-Type"), nil
}

// Identities is where the container agent reads app identities: the host's
// account database, in which the Host Agent creates app-<id> (ADR 0008).
type Identities interface {
	// App returns the account app-<id>; ok is false while it does not exist.
	App(id string) (identity appstore.Identity, ok bool, err error)
	// Unused reports whether uid is free for app-<id>: no account holds it,
	// and no group but app-<id>'s own, which the Host Agent creates first
	// and may have left behind without the account.
	Unused(id string, uid int) (bool, error)
}

// SystemIdentities reads the host's passwd and group databases.
type SystemIdentities struct{}

// App accepts only an account the Host Agent could have made: in the app
// range, with GID equal to UID, its own group, and itself as the account the
// UID resolves to (the first passwd entry with it, as for getent).
func (SystemIdentities) App(id string) (appstore.Identity, bool, error) {
	username := appstore.IdentityName(id)
	account, err := user.Lookup(username)
	if _, missing := errors.AsType[user.UnknownUserError](err); missing {
		return appstore.Identity{}, false, nil
	}
	if err != nil {
		return appstore.Identity{}, false, err
	}
	invalid := fmt.Errorf("account %s is not an A-NAS app identity", username)
	uid, uidErr := strconv.Atoi(account.Uid)
	gid, gidErr := strconv.Atoi(account.Gid)
	if uidErr != nil || gidErr != nil || uid != gid || uid < appstore.FirstAppUID || uid > appstore.LastAppUID {
		return appstore.Identity{}, true, invalid
	}
	group, err := user.LookupGroupId(account.Gid)
	if err != nil {
		return appstore.Identity{}, true, fmt.Errorf("%w: %v", invalid, err)
	}
	owner, err := user.LookupId(account.Uid)
	if err != nil {
		return appstore.Identity{}, true, fmt.Errorf("%w: %v", invalid, err)
	}
	if group.Name != username || owner.Username != username {
		return appstore.Identity{}, true, invalid
	}
	return appstore.Identity{Username: username, UID: uid, GID: gid}, true, nil
}

func (SystemIdentities) Unused(id string, uid int) (bool, error) {
	name := strconv.Itoa(uid)
	if _, err := user.LookupId(name); err == nil {
		return false, nil
	} else if _, missing := errors.AsType[user.UnknownUserIdError](err); !missing {
		return false, err
	}
	group, err := user.LookupGroupId(name)
	if _, missing := errors.AsType[user.UnknownGroupIdError](err); missing {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return group.Name == appstore.IdentityName(id), nil
}

type handler struct {
	store      appstore.Store
	identities Identities
	logger     *slog.Logger
}

// NewHandler serves /v1/apps on the container agent socket. Apps run as the
// identities it reads from identities; nil reads the host's.
func NewHandler(store appstore.Store, identities Identities, logger *slog.Logger) http.Handler {
	if identities == nil {
		identities = SystemIdentities{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &handler{store: store, identities: identities, logger: logger}
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
		httpjson.Write(w, http.StatusOK, AppsFromDomain(apps))
		return
	}
	id, operation, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/apps/"), "/")
	if !ok || !appstore.ValidID(id) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
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
		identity, err := h.planIdentity(id, r.URL.Query().Get("uid"), r.URL.Query().Get("gid"))
		if errors.Is(err, errInvalidIdentity) {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "app identity is invalid")
			return
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		plan, err := h.store.Plan(r.Context(), id, identity)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpjson.Write(w, http.StatusOK, PlanFromDomain(plan))
	case "install-reserve":
		if !allow(w, r, http.MethodPost) {
			return
		}
		var request InstallRequest
		if err := httpjson.Decode(w, r, 1024, &request); err != nil {
			httpjson.WriteDecodeError(w, err, "invalid_request", "request body is invalid")
			return
		}
		identity, err := h.planIdentity(id, strconv.Itoa(request.UID), strconv.Itoa(request.GID))
		if errors.Is(err, errInvalidIdentity) {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "app identity is invalid")
			return
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		reservation, err := h.store.ReserveInstall(r.Context(), id, request.Digest, identity)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpjson.Write(w, http.StatusAccepted, ReservationDocument{Token: reservation.Token, Job: JobFromDomain(reservation.Job)})
	case "install-commit", "install-cancel":
		if !allow(w, r, http.MethodPost) {
			return
		}
		var request PreparationRequest
		if err := httpjson.Decode(w, r, 1024, &request); err != nil {
			httpjson.WriteDecodeError(w, err, "invalid_request", "request body is invalid")
			return
		}
		if operation == "install-cancel" {
			if err := h.store.CancelInstall(r.Context(), id, request.Token); err != nil {
				h.fail(w, r, err)
				return
			}
			httpjson.Write(w, http.StatusOK, struct{}{})
			return
		}
		identity, err := h.installIdentity(id)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		job, err := h.store.CommitInstall(r.Context(), id, request.Token, identity)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpjson.Write(w, http.StatusAccepted, JobFromDomain(job))
	case "install", "uninstall":
		if !allow(w, r, http.MethodPost) {
			return
		}
		var request InstallRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "request body is invalid")
			return
		}
		var job appstore.Job
		var err error
		if operation == "install" {
			// The app runs as its account; the request's UID and GID, which
			// product services still send for older agents, are not used.
			var identity appstore.Identity
			if identity, err = h.installIdentity(id); err == nil {
				job, err = h.store.Install(r.Context(), id, request.Digest, identity)
			}
		} else {
			job, err = h.store.Uninstall(r.Context(), id)
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		h.logger.InfoContext(r.Context(), "app job started", "app", id, "action", job.Action)
		httpjson.Write(w, http.StatusAccepted, JobFromDomain(job))
	default:
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

var errInvalidIdentity = errors.New("app identity is invalid")

// planIdentity is the identity a plan runs as: the app's account. Before the
// first install the account does not exist yet (the Host Agent creates it
// with the app's folders), and the plan shows the UID the Host Agent
// reserved, which the caller passes: it must be in the app range and held by
// no account, nor by a group other than the app's own. ReserveInstall renders
// and checks the plan digest; CommitInstall then requires the real account
// to match that identity before Compose can run.
func (h *handler) planIdentity(id, uid, gid string) (appstore.Identity, error) {
	identity, ok, err := h.identities.App(id)
	if err != nil {
		return appstore.Identity{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	if ok {
		return identity, nil
	}
	reserved, valid := identityFrom(id, uid, gid)
	if !valid {
		return appstore.Identity{}, errInvalidIdentity
	}
	unused, err := h.identities.Unused(id, reserved.UID)
	if err != nil {
		return appstore.Identity{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	if !unused {
		return appstore.Identity{}, errInvalidIdentity
	}
	return reserved, nil
}

// installIdentity is the actual app account, created by the Host Agent before
// the product service commits a reservation (or uses the legacy install).
func (h *handler) installIdentity(id string) (appstore.Identity, error) {
	identity, ok, err := h.identities.App(id)
	if err != nil {
		return appstore.Identity{}, fmt.Errorf("%w: %v", appstore.ErrUnavailable, err)
	}
	if !ok {
		return appstore.Identity{}, fmt.Errorf("%w: account %s does not exist", appstore.ErrUnavailable, appstore.IdentityName(id))
	}
	return identity, nil
}

// identityFrom accepts only the app identity range the Host Agent
// allocates, so a plan never shows a reserved identity of root or a
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
	httpjson.WriteError(w, status, code, err.Error())
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
	{appstore.ErrAddressPoolMissing, http.StatusConflict, "address_pool_missing"},
	{appstore.ErrRuntimeOutdated, http.StatusConflict, "docker_runtime_outdated"},
	{appstore.ErrUnavailable, http.StatusServiceUnavailable, "apps_unavailable"},
}

// StatusForError maps domain errors to codes shared with the product API.
func StatusForError(err error) (int, string) {
	for _, mapping := range errorCodes {
		if errors.Is(err, mapping.err) {
			return mapping.status, mapping.code
		}
	}
	if _, ok := errors.AsType[*appstore.PolicyError](err); ok {
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
	httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorDocument struct {
	Error errorDetail `json:"error"`
}

var _ appstore.Store = (*Client)(nil)
