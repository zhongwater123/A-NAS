package photosapi

import (
	"io"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/activityapi"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// ActivityPath lists the caller's own photo library events for the activity
// log, which the Product Service merges with its own audit trails. It lies
// outside PathPrefix, which is all the Product Service forwards from
// browsers: they read the merged log (#273).
const ActivityPath = "/internal/photos/activity"

// activity answers with up to limit+1 events so the caller can tell whether
// older ones follow.
func (h *handler) activity(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	q, err := activity.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
		return
	}
	events, err := h.service.Activity(r.Context(), principal, q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	activityapi.WriteServicePage(w, events)
}

// DecodeActivity reads an ActivityPath response.
func DecodeActivity(body io.Reader) ([]activity.Event, error) {
	return activityapi.DecodeServicePage(body)
}
