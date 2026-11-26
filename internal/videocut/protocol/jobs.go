package protocol

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// The job routes (docs/architecture/videocut-center.md, 接口): generating
// answers 202 with the job, polled with GET /jobs/{id}.

type generateRequest struct {
	RequestKey string `json:"requestKey"`
}

func (h *handler) generate(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if !h.notePlanGrant(w, r, user) {
		return
	}
	var request generateRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	job, _, err := h.store.Generate(r.Context(), user.ID, r.PathValue("projectID"), request.RequestKey)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusAccepted, job)
}

func (h *handler) job(w http.ResponseWriter, r *http.Request, user accounts.User) {
	job, err := h.store.Job(r.Context(), user.ID, r.PathValue("jobID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, job)
}

func (h *handler) cancelJob(w http.ResponseWriter, r *http.Request, user accounts.User) {
	job, err := h.store.Cancel(r.Context(), user.ID, r.PathValue("jobID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, job)
}

type dropRequest struct {
	AssetIDs []string `json:"assetIds"`
}

func (h *handler) dropAssets(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if !h.notePlanGrant(w, r, user) {
		return
	}
	var request dropRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	job, err := h.store.DropAssets(r.Context(), user.ID, r.PathValue("jobID"), request.AssetIDs)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, job)
}

func (h *handler) retryAnalysis(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if !h.notePlanGrant(w, r, user) {
		return
	}
	job, err := h.store.RetryAnalysis(r.Context(), user.ID, r.PathValue("jobID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, job)
}

func (h *handler) retryJob(w http.ResponseWriter, r *http.Request, user accounts.User) {
	job, err := h.store.Retry(r.Context(), user.ID, r.PathValue("jobID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, job)
}

func (h *handler) replan(w http.ResponseWriter, r *http.Request, user accounts.User) {
	if !h.notePlanGrant(w, r, user) {
		return
	}
	job, err := h.store.Replan(r.Context(), user.ID, r.PathValue("jobID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, job)
}

type noticesResponse struct {
	Items any `json:"items"`
}

func (h *handler) notices(w http.ResponseWriter, r *http.Request, user accounts.User) {
	notices, err := h.store.Notices(r.Context(), user.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, noticesResponse{Items: notices})
}

// notePlanGrant records the planning grant the Product Service registered
// for this request, as AssistantHeader says (planning.go).
func (h *handler) notePlanGrant(w http.ResponseWriter, r *http.Request, user accounts.User) bool {
	if r.Header.Get(AssistantHeader) != "on" {
		return true
	}
	if err := h.store.RegisterPlanGrant(r.Context(), user.ID, user.Username); err != nil {
		h.fail(w, r, err)
		return false
	}
	return true
}
