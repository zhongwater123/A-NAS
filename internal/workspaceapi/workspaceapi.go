// Package workspaceapi serves the shared workspace (ADR 0040) under
// /api/v1/workspace: the desktop's pages, versions, the isolated view and
// export, what a member looks at and picks, the administrators' member list,
// the agents' MCP endpoint and one-time transfer links. It authenticates
// nobody: the workspace service puts the confirmed caller in each request's
// context (workspace.WithCaller), except on transfer links, which carry
// their own authority and are checked here.
package workspaceapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/jpeg" // selection screenshots
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/activityapi"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
	"github.com/zhongwater123/A-NAS/internal/workspace"
	"github.com/zhongwater123/A-NAS/internal/workspacemcp"
)

const (
	PathPrefix  = "/api/v1/workspace"
	AdminPrefix = PathPrefix + "/admin"
	// MCPPath is the agents' endpoint, behind the Product Service's agent
	// posture.
	MCPPath = PathPrefix + "/mcp"
	// TransfersPrefix holds one-time links, public on the Product Service:
	// the link's key is the authority.
	TransfersPrefix = PathPrefix + "/transfers/"
	// ActivityPath is internal to the Product Service's merged activity log.
	ActivityPath = "/internal/workspace/activity"
	// Unavailable is the error code while the store cannot be used.
	Unavailable = "workspace_unavailable"
	// AgentTokenHeader carries an agent's token from the Product Service,
	// which set it after its own check; the service checks it again.
	AgentTokenHeader = serviceproxy.AgentTokenHeader
	// OriginHeader carries the scheme and host the agent reached the NAS at,
	// for the transfer links the MCP tools hand out.
	OriginHeader = serviceproxy.AgentOriginHeader
	// FrameScript is the helper script every isolated page loads
	// (web/src/workspace/frame), served by internal/webui.
	FrameScript = "/assets/workspace-frame.js"
)

// AgentRoutes are the MCP endpoint's methods, the Product Service's own
// routes under its agent posture: Streamable HTTP uses all three.
var AgentRoutes = []apiroute.Endpoint{
	{Method: http.MethodPost, Pattern: MCPPath}, {Method: http.MethodGet, Pattern: MCPPath}, {Method: http.MethodDelete, Pattern: MCPPath},
}

// LinkRoutes are the one-time transfer links, public on the Product Service.
var LinkRoutes = []apiroute.Endpoint{
	{Method: http.MethodGet, Pattern: TransfersPrefix + "{key}"}, {Method: http.MethodPut, Pattern: TransfersPrefix + "{key}"},
}

// framePolicy runs a page's own scripts and styles in an opaque origin that
// reaches nothing (ADR 0040 decision 2).
const framePolicy = "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; " +
	"img-src data: blob:; font-src data:; media-src data: blob:; connect-src data: blob:; form-action 'none'; base-uri 'none'; frame-ancestors 'self'"

// filePolicy keeps a downloaded page from running as part of the desktop
// should a browser show it instead of saving it.
const filePolicy = "sandbox; default-src 'none'"

// Body limits: JSON with a selection's screenshot, and a page.
const (
	maxJSONBytes      = 1 << 20
	maxSelectionImage = 512 << 10
)

// Directory is what the API asks the Host Agent: the active accounts, for
// administrators choosing members, and whether the agent token behind a
// transfer link still works.
type Directory interface {
	ActiveProfiles(context.Context) ([]accountid.Profile, error)
	AgentTokenByID(ctx context.Context, id, scope string) (accountid.AgentTokenUser, error)
}

// Options configures the API.
type Options struct {
	Logger    *slog.Logger
	Directory Directory
	// FrameScript is the helper's address; FrameScript when empty.
	FrameScript string
}

type handler struct {
	store   *workspace.Store
	options Options
	mux     *apiroute.Mux
	mcp     http.Handler
}

type route func(http.ResponseWriter, *http.Request, workspace.Caller)

// New returns the API. A nil store answers 503 workspace_unavailable.
func New(store *workspace.Store, options Options) http.Handler {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.FrameScript == "" {
		options.FrameScript = FrameScript
	}
	h := &handler{store: store, options: options, mux: apiroute.NewMux()}
	if store != nil {
		h.mcp = workspacemcp.New(store, workspacemcp.Options{Logger: options.Logger, TransferPath: TransfersPrefix})
	}
	members := map[string]route{
		"GET ":                          h.access,
		"GET /pages":                    h.listPages,
		"POST /pages":                   h.createPage,
		"GET /pages/{pageID}":           h.page,
		"PATCH /pages/{pageID}":         h.renamePage,
		"DELETE /pages/{pageID}":        h.deletePage,
		"POST /pages/{pageID}/versions": h.saveVersion,
		"GET /pages/{pageID}/versions/{version}/view": h.view,
		"GET /pages/{pageID}/versions/{version}/file": h.file,
		"PUT /viewing":             h.viewing,
		"DELETE /viewing/{pageID}": h.stopViewing,
		"PUT /selection":           h.setSelection,
		"DELETE /selection":        h.clearSelection,
	}
	for pattern, serve := range members {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+PathPrefix+path, h.member(serve))
	}
	for pattern, serve := range map[string]route{
		"GET /members":                h.listMembers,
		"PUT /members/{accountID}":    h.addMember,
		"DELETE /members/{accountID}": h.removeMember,
	} {
		method, path, _ := strings.Cut(pattern, " ")
		h.mux.HandleFunc(method+" "+AdminPrefix+path, h.administrator(serve))
	}
	// The agents' and the links' operations are the Product Service's own
	// routes, not part of the desktop's mount.
	for _, route := range AgentRoutes {
		h.mux.ServeMux.HandleFunc(route.Method+" "+route.Pattern, h.member(h.agent))
	}
	links := map[string]http.HandlerFunc{http.MethodGet: h.download, http.MethodPut: h.upload}
	for _, route := range LinkRoutes {
		h.mux.ServeMux.HandleFunc(route.Method+" "+route.Pattern, links[route.Method])
	}
	h.mux.ServeMux.HandleFunc("GET "+ActivityPath, h.signedIn(h.activity))
	h.mux.HandleFunc(PathPrefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

// Endpoints are the desktop's operations under PathPrefix and AdminPrefix.
func Endpoints() []apiroute.Endpoint {
	return New(nil, Options{}).(*handler).mux.Endpoints()
}

// MemberEndpoints are the operations under PathPrefix, mounted for every
// member who may use the workspace.
func MemberEndpoints() []apiroute.Endpoint { return slices.DeleteFunc(Endpoints(), administrators) }

// AdminEndpoints are the administrators' operations under AdminPrefix.
func AdminEndpoints() []apiroute.Endpoint {
	return slices.DeleteFunc(Endpoints(), func(e apiroute.Endpoint) bool { return !administrators(e) })
}

func administrators(endpoint apiroute.Endpoint) bool {
	return strings.HasPrefix(endpoint.Pattern, AdminPrefix+"/")
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "the shared workspace is not available")
		return
	}
	h.mux.ServeHTTP(w, r)
}

// signedIn finds the caller the service confirmed.
func (h *handler) signedIn(next route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, ok := workspace.CallerFrom(r.Context())
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "authentication is required")
			return
		}
		next(w, r, caller)
	}
}

// member lets through those who may use the workspace: administrators and
// the members they added. The desktop learns of the others by a 404 from
// GET /api/v1/workspace; everything else answers 403.
func (h *handler) member(next route) http.HandlerFunc {
	return h.signedIn(func(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
		ok, err := h.store.CanUse(r.Context(), caller.Actor)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if !ok {
			if r.Method == http.MethodGet && r.URL.Path == PathPrefix {
				httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
				return
			}
			httpjson.WriteError(w, http.StatusForbidden, "workspace_forbidden", "an administrator has not let this account use the shared workspace")
			return
		}
		next(w, r, caller)
	})
}

// administrator lets through active administrators, whatever the Product
// Service's route guard already checked.
func (h *handler) administrator(next route) http.HandlerFunc {
	return h.signedIn(func(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
		if !caller.Actor.Admin || caller.Agent() {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "administrator permission is required")
			return
		}
		next(w, r, caller)
	})
}

func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var changed *workspace.PageChangedError
	switch {
	case errors.As(err, &changed):
		httpjson.Write(w, http.StatusConflict, map[string]any{
			"error":  map[string]string{"code": "page_changed", "message": changed.Error()},
			"latest": changed.Latest,
		})
	case errors.Is(err, workspace.ErrNotFound):
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, workspace.ErrForbidden):
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "only the member who uploaded the page and administrators may do this")
	case errors.Is(err, workspace.ErrTooLarge):
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "request_too_large", "a page is at most 20 MB")
	case errors.Is(err, workspace.ErrInvalid):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", strings.TrimPrefix(err.Error(), workspace.ErrInvalid.Error()+": "))
	case errors.Is(err, workspace.ErrVolumeFull):
		httpjson.WriteError(w, http.StatusInsufficientStorage, "insufficient_storage", "the data volume has no room left")
	default:
		httpjson.WriteInternal(w, r, h.options.Logger, err)
	}
}

func (h *handler) access(w http.ResponseWriter, _ *http.Request, caller workspace.Caller) {
	httpjson.Write(w, http.StatusOK, map[string]any{"admin": caller.Actor.Admin})
}

func (h *handler) listPages(w http.ResponseWriter, r *http.Request, _ workspace.Caller) {
	pages, err := h.store.ListPages(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": pages})
}

// readPage reads a page sent as the request body.
func readPage(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	content, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workspace.MaxPageBytes))
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		httpjson.WriteError(w, http.StatusRequestEntityTooLarge, "request_too_large", "a page is at most 20 MB")
		return nil, false
	}
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "the page could not be read")
		return nil, false
	}
	return content, true
}

func (h *handler) createPage(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	content, ok := readPage(w, r)
	if !ok {
		return
	}
	page, err := h.store.CreatePage(r.Context(), caller.Actor, r.URL.Query().Get("name"), content, caller.Via, "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, page)
}

func (h *handler) page(w http.ResponseWriter, r *http.Request, _ workspace.Caller) {
	page, err := h.store.Page(r.Context(), r.PathValue("pageID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, page)
}

func (h *handler) renamePage(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	var request struct {
		Title string `json:"title"`
	}
	if !httpjson.Read(w, r, 4<<10, &request) {
		return
	}
	page, err := h.store.RenamePage(r.Context(), caller.Actor, r.PathValue("pageID"), request.Title)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, page)
}

func (h *handler) deletePage(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	if err := h.store.DeletePage(r.Context(), caller.Actor, r.PathValue("pageID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) saveVersion(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	base, err := strconv.Atoi(r.URL.Query().Get("baseVersion"))
	if err != nil || base < 1 {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "give baseVersion, the version the page replaces")
		return
	}
	content, ok := readPage(w, r)
	if !ok {
		return
	}
	version, err := h.store.SaveVersion(r.Context(), caller.Actor, r.PathValue("pageID"), base, content, caller.Via, "")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, version)
}

// version reads the version in the path; "latest" names the latest.
func version(r *http.Request) (int, bool) {
	value := r.PathValue("version")
	if value == "latest" {
		return 0, true
	}
	n, err := strconv.Atoi(value)
	return n, err == nil && n > 0
}

// view serves a version for the sandboxed frame: the page with its elements
// marked and the helper script, under a policy that leaves it an opaque
// origin which reaches nothing.
func (h *handler) view(w http.ResponseWriter, r *http.Request, _ workspace.Caller) {
	n, ok := version(r)
	if !ok {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	source, summary, err := h.store.Content(r.Context(), r.PathValue("pageID"), n)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	query := r.URL.Query()
	helper := fmt.Sprintf(`<script src="%s" data-page="%s" data-highlight="%s" data-scroll="%s"></script>`,
		html.EscapeString(h.options.FrameScript), html.EscapeString(summary.ID), numbers(query.Get("highlight")), numbers(query.Get("scroll")))
	header := w.Header()
	header.Set("Content-Security-Policy", framePolicy)
	header.Set("X-Frame-Options", "SAMEORIGIN")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(workspace.Annotate(source, helper))
}

// file serves a version as the file it was saved as.
func (h *handler) file(w http.ResponseWriter, r *http.Request, _ workspace.Caller) {
	n, ok := version(r)
	if !ok {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	source, summary, err := h.store.Content(r.Context(), r.PathValue("pageID"), n)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	serveFile(w, source, summary)
}

func serveFile(w http.ResponseWriter, source []byte, summary workspace.PageSummary) {
	name := fmt.Sprintf("%s-v%d.html", safeName(summary.Title), summary.Latest.Version)
	header := w.Header()
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", filePolicy)
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "no-store")
	header.Set("Content-Length", strconv.Itoa(len(source)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(source)
}

func (h *handler) viewing(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	var request struct {
		PageID  string `json:"pageId"`
		Version int    `json:"version"`
	}
	if !httpjson.Read(w, r, 4<<10, &request) {
		return
	}
	if _, _, err := h.store.Version(r.Context(), request.PageID, request.Version); err != nil {
		h.fail(w, r, err)
		return
	}
	h.store.SetViewing(caller.Actor.AccountID, request.PageID, request.Version)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) stopViewing(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	h.store.ClearViewing(caller.Actor.AccountID, r.PathValue("pageID"))
	w.WriteHeader(http.StatusNoContent)
}

// selectionRequest is what the desktop reports when a member picks an
// element; the frame helper measured it (web/src/workspace/frame/protocol.ts).
type selectionRequest struct {
	PageID    string            `json:"pageId"`
	Version   int               `json:"version"`
	At        int               `json:"at"`
	Generated bool              `json:"generated"`
	Tag       string            `json:"tag"`
	Text      string            `json:"text"`
	DOM       string            `json:"dom"`
	Rect      workspace.Rect    `json:"rect"`
	Style     map[string]string `json:"style"`
	Rules     []string          `json:"rules"`
	Image     string            `json:"image"`
}

func (h *handler) setSelection(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	var request selectionRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	if _, _, err := h.store.Version(r.Context(), request.PageID, request.Version); err != nil {
		h.fail(w, r, err)
		return
	}
	picture, err := selectionImage(request.Image)
	if err != nil {
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
		return
	}
	if len(request.Rules) > 20 || len(request.Style) > 20 {
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "validation_failed", "at most 20 rules and 20 style properties")
		return
	}
	h.store.SetSelection(caller.Actor.AccountID, workspace.Selection{
		PageID: request.PageID, Version: request.Version, At: request.At, Generated: request.Generated,
		Tag: workspace.Cut(request.Tag, 40), Text: workspace.Cut(request.Text, 2000), DOM: workspace.Cut(request.DOM, 8000),
		Rect: request.Rect, Style: request.Style, Rules: request.Rules, Image: picture,
	})
	w.WriteHeader(http.StatusNoContent)
}

// selectionImage decodes the screenshot of a selection, a JPEG data URL of
// at most maxSelectionImage bytes; only its header is read, never its pixels.
func selectionImage(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	encoded, ok := strings.CutPrefix(value, "data:image/jpeg;base64,")
	if !ok || base64.StdEncoding.DecodedLen(len(encoded)) > maxSelectionImage {
		return nil, errors.New("the screenshot must be a JPEG of at most 512 KB")
	}
	picture, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("the screenshot is not valid base64")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(picture))
	if err != nil || format != "jpeg" || config.Width > 4096 || config.Height > 4096 {
		return nil, errors.New("the screenshot must be a JPEG of at most 4096 pixels a side")
	}
	return picture, nil
}

func (h *handler) clearSelection(w http.ResponseWriter, _ *http.Request, caller workspace.Caller) {
	h.store.ClearSelection(caller.Actor.AccountID)
	w.WriteHeader(http.StatusNoContent)
}

// agent hands an agent's call to the MCP endpoint.
func (h *handler) agent(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	if !caller.Agent() {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "an agent token is required")
		return
	}
	h.mcp.ServeHTTP(w, r)
}

// takeTransfer uses up a link and confirms that the agent token behind it
// still lets its account use the workspace.
func (h *handler) takeTransfer(w http.ResponseWriter, r *http.Request, direction workspace.Direction) (workspace.Transfer, bool) {
	transfer, err := h.store.TakeTransfer(r.Context(), r.PathValue("key"), direction)
	if errors.Is(err, workspace.ErrNotFound) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "the link is unknown, used or expired; ask for a new one")
		return workspace.Transfer{}, false
	}
	if err != nil {
		h.fail(w, r, err)
		return workspace.Transfer{}, false
	}
	if h.options.Directory == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "agent tokens cannot be checked")
		return workspace.Transfer{}, false
	}
	token, err := h.options.Directory.AgentTokenByID(r.Context(), transfer.Via.TokenID, accountid.AgentScopeWorkspace)
	if errors.Is(err, accountid.ErrAgentTokenNotFound) || err == nil && token.UserID != transfer.Actor.AccountID {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "the agent token behind the link no longer works")
		return workspace.Transfer{}, false
	}
	if err != nil {
		h.options.Logger.WarnContext(r.Context(), "agent token lookup failed", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "agent tokens cannot be checked")
		return workspace.Transfer{}, false
	}
	transfer.Actor.Admin = token.Role == accountid.RoleAdmin
	if ok, err := h.store.CanUse(r.Context(), transfer.Actor); err != nil || !ok {
		httpjson.WriteError(w, http.StatusForbidden, "workspace_forbidden", "an administrator has not let this account use the shared workspace")
		return workspace.Transfer{}, false
	}
	return transfer, true
}

func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	// A GET pattern also matches HEAD; a link checker or `curl -I` must not
	// spend the link.
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		httpjson.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	transfer, ok := h.takeTransfer(w, r, workspace.Download)
	if !ok {
		return
	}
	source, summary, err := h.store.Content(r.Context(), transfer.PageID, transfer.Version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	serveFile(w, source, summary)
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	transfer, ok := h.takeTransfer(w, r, workspace.Upload)
	if !ok {
		return
	}
	content, ok := readPage(w, r)
	if !ok {
		return
	}
	if transfer.PageID == "" {
		page, err := h.store.CreatePage(r.Context(), transfer.Actor, "", content, transfer.Via, transfer.Note)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		httpjson.Write(w, http.StatusCreated, map[string]any{"page": page.ID, "title": page.Title, "version": page.Latest.Version})
		return
	}
	version, err := h.store.SaveVersion(r.Context(), transfer.Actor, transfer.PageID, transfer.Version, content, transfer.Via, transfer.Note)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, map[string]any{"page": transfer.PageID, "version": version.Version})
}

func (h *handler) listMembers(w http.ResponseWriter, r *http.Request, _ workspace.Caller) {
	if h.options.Directory == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, Unavailable, "the accounts cannot be listed")
		return
	}
	profiles, err := h.options.Directory.ActiveProfiles(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	added, err := h.store.MemberIDs(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	type account struct {
		accountid.Profile
		Member bool `json:"member"`
	}
	items := make([]account, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, account{Profile: profile, Member: added[profile.ID]})
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"items": items})
}

func (h *handler) addMember(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	if err := h.store.AddMember(r.Context(), caller.Actor, r.PathValue("accountID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) removeMember(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	if err := h.store.RemoveMember(r.Context(), caller.Actor, r.PathValue("accountID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) activity(w http.ResponseWriter, r *http.Request, caller workspace.Caller) {
	q, err := activity.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
		return
	}
	events, err := h.store.Activity(r.Context(), caller.Actor, q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	activityapi.WriteServicePage(w, events)
}

// numbers keeps only a comma-separated list of numbers.
func numbers(value string) string {
	var kept []string
	for part := range strings.SplitSeq(value, ",") {
		if _, err := strconv.Atoi(part); err == nil && len(kept) < 50 {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, ",")
}

// safeName makes a title usable as a file name.
func safeName(title string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, title)
	if name == "" {
		return "page"
	}
	return name
}
