package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// systemActor is the actor the file and storage modules record for what the
// device does on its own, such as emptying expired trash.
const systemActor = "system"

type activityAccount struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname,omitempty"`
}

type activitySpace struct {
	ID    string             `json:"id"`
	Kind  accounts.SpaceKind `json:"kind"`
	Owner *activityAccount   `json:"owner,omitempty"`
}

type activityItem struct {
	ID         string            `json:"id"`
	OccurredAt time.Time         `json:"occurredAt"`
	Category   activity.Category `json:"category"`
	Action     string            `json:"action"`
	Actor      *activityAccount  `json:"actor,omitempty"`
	// System marks what the device did on its own.
	System bool `json:"system,omitempty"`
	// Subject is the account an account or sign-in event was about.
	Subject      *activityAccount `json:"subject,omitempty"`
	ResourceType string           `json:"resourceType"`
	ResourceID   string           `json:"resourceId,omitempty"`
	Detail       string           `json:"detail,omitempty"`
	Space        *activitySpace   `json:"space,omitempty"`
	// Redacted says names in another member's private space were left out.
	Redacted bool `json:"redacted,omitempty"`
}

type activityResponse struct {
	Items []activityItem `json:"items"`
	Next  string         `json:"next,omitempty"`
	// Scope is "all" for administrators and "self" for members.
	Scope string `json:"scope"`
	// Unavailable lists the categories whose records could not be read.
	Unavailable []activity.Category `json:"unavailable,omitempty"`
}

// handleActivity serves one page of the activity log: every account's events
// to administrators, and only their own to members. Each module's audit trail
// is read with the same filters and cursor and merged newest first; a module
// that fails is reported instead of failing the page.
func (h *productHandler) handleActivity(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	ctx := r.Context()
	q, err := activity.ParseQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
		return
	}
	directory, err := h.accounts.Directory(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	viewer := session.User
	admin := viewer.Role == accounts.RoleAdmin
	scope := "self"
	// actorFilter is whose events were asked for; empty means everyone's.
	actorFilter := viewer.ID
	if admin {
		scope, actorFilter = "all", r.URL.Query().Get("actor")
		if _, known := directory.Users[actorFilter]; actorFilter != "" && actorFilter != systemActor && !known {
			writeError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
			return
		}
		reveal := &activity.Reveal{ViewerID: viewer.ID}
		for _, space := range directory.Spaces {
			if space.Kind == accounts.SpaceKindShared || space.OwnerUserID == viewer.ID {
				reveal.SpaceIDs = append(reveal.SpaceIDs, space.ID)
			}
		}
		q.Reveal = reveal
	}
	if actorFilter == systemActor {
		q.Subject = &activity.Subject{ActorIDs: []string{systemActor}}
	} else if actorFilter != "" {
		if q.Subject, err = h.accounts.ActivitySubject(ctx, actorFilter); err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	if q.Text != "" {
		text := strings.ToLower(q.Text)
		for id, user := range directory.Users {
			if strings.Contains(user.Username, text) || strings.Contains(strings.ToLower(user.Nickname), text) {
				q.TextActors = append(q.TextActors, id)
			}
		}
	}
	// Other modules only know who acted, not which account was acted on.
	actorsOnly := q
	if q.Subject != nil {
		actorsOnly.Subject = &activity.Subject{ActorIDs: q.Subject.ActorIDs}
	}

	var pages [][]activity.Event
	var unavailable []activity.Category
	read := func(categories []activity.Category, list func() ([]activity.Event, error)) {
		if !q.Wants(categories...) {
			return
		}
		events, err := list()
		if err != nil {
			h.logger.WarnContext(ctx, "activity source unavailable", "categories", categories, "error", err)
			unavailable = append(unavailable, categories...)
			return
		}
		pages = append(pages, events)
	}
	read(accounts.ActivityCategories, func() ([]activity.Event, error) { return h.accounts.Activity(ctx, viewer, q) })
	if h.files != nil {
		read(files.ActivityCategories, func() ([]activity.Event, error) { return h.files.Activity(ctx, viewer, actorsOnly) })
	}
	if admin && h.storage != nil {
		read(storage.ActivityCategories, func() ([]activity.Event, error) { return h.storage.Activity(ctx, actorsOnly) })
	}
	// Photo events are only ever shown to the account that made them.
	if h.photos != nil && (actorFilter == "" || actorFilter == viewer.ID) {
		read([]activity.Category{activity.CategoryPhoto}, func() ([]activity.Event, error) { return h.photoActivity(r, q) })
	}
	events, more := activity.Merge(q.Limit, pages...)

	response := activityResponse{Items: make([]activityItem, 0, len(events)), Scope: scope, Unavailable: unavailable}
	for _, event := range events {
		response.Items = append(response.Items, presentActivity(event, directory, q.Reveal))
	}
	if more {
		response.Next = events[len(events)-1].Key.String()
	}
	writeJSON(w, http.StatusOK, response)
}

func presentActivity(event activity.Event, directory accounts.Directory, reveal *activity.Reveal) activityItem {
	account := func(id string) *activityAccount {
		if user, ok := directory.Users[id]; ok {
			return &activityAccount{ID: id, Username: user.Username, Nickname: user.Nickname}
		}
		return nil
	}
	item := activityItem{
		ID: event.ID, OccurredAt: event.OccurredAt, Category: activity.CategoryOf(event.Action), Action: event.Action,
		Actor: account(event.ActorUserID), System: event.ActorUserID == systemActor,
		ResourceType: event.ResourceType, ResourceID: event.ResourceID, Detail: event.Detail,
	}
	spaceID := event.SpaceID
	switch event.ResourceType {
	case "user":
		item.Subject = account(event.ResourceID)
	case "space":
		spaceID = event.ResourceID
	}
	if space, ok := directory.Spaces[spaceID]; ok {
		item.Space = &activitySpace{ID: space.ID, Kind: space.Kind, Owner: account(space.OwnerUserID)}
	}
	if item.Category == activity.CategoryFile && !reveal.Revealed(event) {
		item.Redacted, item.ResourceID, item.Detail = true, "", ""
	}
	return item
}

// photoActivity reads the caller's photo events through the photo API, so it
// works whether the library runs in this process or in the photo service.
func (h *productHandler) photoActivity(r *http.Request, q activity.Query) ([]activity.Event, error) {
	request := r.Clone(r.Context())
	request.Method, request.Body, request.RequestURI = http.MethodGet, http.NoBody, ""
	request.URL = &url.URL{Path: photosapi.ActivityPath, RawQuery: q.Values().Encode()}
	response := &bufferedResponse{header: http.Header{}}
	h.withPhotoPrincipal(h.photos)(response, request)
	if response.status != http.StatusOK {
		return nil, fmt.Errorf("photo activity status %d", response.status)
	}
	return photosapi.DecodeActivity(&response.body)
}

// bufferedResponse keeps a response in memory for a request served in-process.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(data []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(data)
}

// withClientOrigin records where a sign-in or sign-out came from.
func withClientOrigin(r *http.Request) context.Context {
	return accounts.WithClientOrigin(r.Context(), clientOrigin(r))
}

// clientOrigin is where a request came from, which sessions are also bound
// to. The LAN entry (Caddy, ADR 0012) is the only proxy and sets
// X-Forwarded-For to the client's address; the Product Service listens on
// loopback only, so anything else connected over loopback directly.
func clientOrigin(r *http.Request) accounts.ClientOrigin {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" && r.Header.Get("Forwarded") == "" {
		return accounts.ClientOrigin{Via: accounts.ViaLoopback}
	}
	origin := accounts.ClientOrigin{Via: accounts.ViaLAN}
	hops := strings.Split(forwarded, ",")
	address := strings.TrimSpace(hops[len(hops)-1])
	// Caddy writes a link-local client's address with its zone.
	if _, err := netip.ParseAddr(address); err == nil {
		origin.Address = address
	}
	return origin
}
