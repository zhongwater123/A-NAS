package photosapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
)

// ActivityPath lists the caller's own photo library events for the activity
// log, which the Product Service merges with its own audit trails.
const ActivityPath = PathPrefix + "/activity"

type activityEvent struct {
	ID           string    `json:"id"`
	ActorUserID  string    `json:"actorUserId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	OccurredAt   time.Time `json:"occurredAt"`
	Detail       string    `json:"detail,omitempty"`
	Cursor       string    `json:"cursor"`
}

// activity answers with up to limit+1 events so the caller can tell whether
// older ones follow.
func (h *handler) activity(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	q, err := activity.ParseQuery(r.URL.Query())
	if err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
		return
	}
	events, err := h.service.Activity(r.Context(), principal, q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]activityEvent, 0, len(events))
	for _, event := range events {
		items = append(items, activityEvent{
			ID: event.ID, ActorUserID: event.ActorUserID, Action: event.Action, ResourceType: event.ResourceType,
			ResourceID: event.ResourceID, OccurredAt: event.OccurredAt, Detail: event.Detail, Cursor: event.Key.String(),
		})
	}
	writeJSON(w, http.StatusOK, itemsResponse[activityEvent]{Items: items})
}

// DecodeActivity reads an ActivityPath response.
func DecodeActivity(body io.Reader) ([]activity.Event, error) {
	var document itemsResponse[activityEvent]
	if err := json.NewDecoder(io.LimitReader(body, 8<<20)).Decode(&document); err != nil {
		return nil, err
	}
	events := make([]activity.Event, 0, len(document.Items))
	for _, item := range document.Items {
		key, err := activity.ParseCursor(item.Cursor)
		if err != nil || key.ID != item.ID {
			return nil, errors.New("photo activity has an invalid cursor")
		}
		events = append(events, activity.Event{
			ID: item.ID, ActorUserID: item.ActorUserID, Action: item.Action, ResourceType: item.ResourceType,
			ResourceID: item.ResourceID, OccurredAt: item.OccurredAt, Detail: item.Detail, Key: key,
		})
	}
	return events, nil
}
