package activityapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

type wireEvent struct {
	ID           string    `json:"id"`
	ActorUserID  string    `json:"actorUserId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	OccurredAt   time.Time `json:"occurredAt"`
	Detail       string    `json:"detail,omitempty"`
	Cursor       string    `json:"cursor"`
}

// WriteServicePage encodes a service page for the merged activity log.
func WriteServicePage(w http.ResponseWriter, events []activity.Event) {
	items := make([]wireEvent, 0, len(events))
	for _, e := range events {
		items = append(items, wireEvent{ID: e.ID, ActorUserID: e.ActorUserID, Action: e.Action, ResourceType: e.ResourceType, ResourceID: e.ResourceID, OccurredAt: e.OccurredAt, Detail: e.Detail, Cursor: e.Key.String()})
	}
	httpjson.Write(w, http.StatusOK, struct {
		Items []wireEvent `json:"items"`
	}{items})
}

// DecodeServicePage reads a service page written by WriteServicePage.
func DecodeServicePage(body io.Reader) ([]activity.Event, error) {
	var document struct {
		Items []wireEvent `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 8<<20)).Decode(&document); err != nil {
		return nil, err
	}
	events := make([]activity.Event, 0, len(document.Items))
	for _, item := range document.Items {
		key, err := activity.ParseCursor(item.Cursor)
		if err != nil || key.ID != item.ID {
			return nil, errors.New("activity has an invalid cursor")
		}
		events = append(events, activity.Event{
			ID: item.ID, ActorUserID: item.ActorUserID, Action: item.Action, ResourceType: item.ResourceType,
			ResourceID: item.ResourceID, OccurredAt: item.OccurredAt, Detail: item.Detail, Key: key,
		})
	}
	return events, nil
}
