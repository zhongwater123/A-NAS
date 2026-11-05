package familyapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// ActivityPath is internal to the Product Service's merged activity log.
const ActivityPath = "/internal/family/activity"

func (h *handler) activity(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	q, err := activity.ParseQuery(r.URL.Query())
	if err != nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_query", "activity filters are invalid")
		return
	}
	events, err := h.store.Activity(r.Context(), actor, q)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	activity.Write(w, events)
}
