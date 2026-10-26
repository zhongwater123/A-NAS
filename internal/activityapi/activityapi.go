// Package activityapi serves the activity log (docs/specs/logs.md): every
// account's events to administrators and only their own to members, merged
// newest first from each module's audit trail.
package activityapi

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/storage"
)

// Sources are the modules whose audit trails the log merges; nil ones are
// left out.
type Sources struct {
	Accounts *accounts.Service
	Files    *files.Service
	Storage  *storage.Service
	// Photos reads the caller's photo events through the photo API, so it
	// works whether the library runs in this process or in the photo service.
	Photos func(r *http.Request, q activity.Query) ([]activity.Event, error)
	Logger *slog.Logger
}

// API serves the log.
type API struct {
	accounts *accounts.Service
	files    *files.Service
	storage  *storage.Service
	photos   func(*http.Request, activity.Query) ([]activity.Event, error)
	logger   *slog.Logger
}

func New(sources Sources) *API {
	return &API{accounts: sources.Accounts, files: sources.Files, storage: sources.Storage, photos: sources.Photos, logger: sources.Logger}
}

func (a *API) Routes() []apiroute.Route {
	return []apiroute.Route{
		{Method: http.MethodGet, Pattern: "/api/v1/activity", Posture: apiroute.Session, Handler: a.activity},
	}
}

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
func (a *API) activity(w http.ResponseWriter, r *http.Request, session accounts.Session) {
	ctx := r.Context()
	q, err := activity.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
		return
	}
	directory, err := a.accounts.Directory(ctx)
	if err != nil {
		httpjson.WriteInternal(w, r, a.logger, err)
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
			httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
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
		if q.Subject, err = a.accounts.ActivitySubject(ctx, actorFilter); err != nil {
			httpjson.WriteInternal(w, r, a.logger, err)
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
			a.logger.WarnContext(ctx, "activity source unavailable", "categories", categories, "error", err)
			unavailable = append(unavailable, categories...)
			return
		}
		pages = append(pages, events)
	}
	read(accounts.ActivityCategories, func() ([]activity.Event, error) { return a.accounts.Activity(ctx, viewer, q) })
	if a.files != nil {
		read(files.ActivityCategories, func() ([]activity.Event, error) { return a.files.Activity(ctx, viewer, actorsOnly) })
	}
	if admin && a.storage != nil {
		read(storage.ActivityCategories, func() ([]activity.Event, error) { return a.storage.Activity(ctx, actorsOnly) })
	}
	// Photo events are only ever shown to the account that made them.
	if a.photos != nil && (actorFilter == "" || actorFilter == viewer.ID) {
		read([]activity.Category{activity.CategoryPhoto}, func() ([]activity.Event, error) { return a.photos(r, q) })
	}
	events, more := activity.Merge(q.Limit, pages...)

	response := activityResponse{Items: make([]activityItem, 0, len(events)), Scope: scope, Unavailable: unavailable}
	for _, event := range events {
		response.Items = append(response.Items, presentActivity(event, directory, q.Reveal))
	}
	if more {
		response.Next = events[len(events)-1].Key.String()
	}
	httpjson.Write(w, http.StatusOK, response)
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
