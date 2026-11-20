package montageapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/montage"
)

// Editing a film after it was made (#596; docs/architecture/montage-center.md,
// 出片后的二次编辑): confirming a version, its draft and the steps of the
// edit, candidates for a shot, the shot lists and thumbnails.

func (h *handler) confirmVersion(w http.ResponseWriter, r *http.Request, user accounts.User) {
	detail, err := h.store.ConfirmVersion(r.Context(), user.ID, r.PathValue("versionID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, detail)
}

func (h *handler) versionDraft(w http.ResponseWriter, r *http.Request, user accounts.User) {
	draft, err := h.store.VersionDraft(r.Context(), user.ID, r.PathValue("versionID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, draft)
}

type draftOpRequest struct {
	BaseRevision *int            `json:"baseRevision"`
	Op           montage.DraftOp `json:"op"`
}

func (h *handler) editDraft(w http.ResponseWriter, r *http.Request, user accounts.User) {
	var request draftOpRequest
	if !httpjson.Read(w, r, maxJSONBytes, &request) {
		return
	}
	if request.BaseRevision == nil || *request.BaseRevision < 0 || request.Op.Seed != nil && *request.Op.Seed < 0 {
		h.fail(w, r, montage.ErrInvalid)
		return
	}
	draft, err := h.store.EditDraft(r.Context(), user.ID, r.PathValue("draftID"), *request.BaseRevision, request.Op)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, draft)
}

func (h *handler) draftCandidates(w http.ResponseWriter, r *http.Request, user accounts.User) {
	values := r.URL.Query()
	query := montage.CandidateQuery{Q: values.Get("q"), Size: values.Get("size"), Scene: values.Get("scene")}
	var err error
	if query.Shot, err = strconv.Atoi(values.Get("shot")); err != nil {
		h.fail(w, r, montage.ErrInvalid)
		return
	}
	if landscape := values.Get("landscape"); landscape != "" {
		if query.Landscape, err = strconv.ParseBool(landscape); err != nil {
			h.fail(w, r, montage.ErrInvalid)
			return
		}
	}
	candidates, err := h.store.DraftCandidates(r.Context(), user.ID, r.PathValue("draftID"), query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, candidates)
}

func (h *handler) versionShots(w http.ResponseWriter, r *http.Request, user accounts.User) {
	list, err := h.store.VersionShots(r.Context(), user.ID, r.PathValue("versionID"))
	h.shotList(w, r, list, err)
}

func (h *handler) draftShots(w http.ResponseWriter, r *http.Request, user accounts.User) {
	list, err := h.store.DraftShots(r.Context(), user.ID, r.PathValue("draftID"))
	h.shotList(w, r, list, err)
}

func (h *handler) shotList(w http.ResponseWriter, r *http.Request, list []byte, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="shots.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(list)
}

func (h *handler) frame(w http.ResponseWriter, r *http.Request, user accounts.User) {
	ms, err := strconv.ParseInt(r.URL.Query().Get("t"), 10, 64)
	if err != nil {
		h.fail(w, r, montage.ErrInvalid)
		return
	}
	jpeg, err := h.store.Thumbnail(r.Context(), user.ID, r.PathValue("projectID"), r.PathValue("assetID"), ms)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	// The video's content never changes: the same instant is the same frame.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(jpeg)
}

// failEdit answers the editing errors; it reports whether err was one.
func failEdit(w http.ResponseWriter, err error) bool {
	var refusal *montage.EditRefusal
	switch {
	case errors.As(err, &refusal):
		httpjson.WriteError(w, http.StatusUnprocessableEntity, "edit_refused", refusal.Reason)
	case errors.Is(err, montage.ErrDraftConflict):
		httpjson.WriteError(w, http.StatusConflict, "draft_conflict", "the draft changed since; read it again")
	case errors.Is(err, montage.ErrNotEditable):
		httpjson.WriteError(w, http.StatusConflict, "not_editable", "the version can only be played")
	case errors.Is(err, montage.ErrSearchModel):
		httpjson.WriteError(w, http.StatusConflict, "search_model_changed", "分析模型已更新，重新分析后才能搜索")
	case errors.Is(err, montage.ErrSearchUnavailable):
		httpjson.WriteError(w, http.StatusServiceUnavailable, "search_unavailable", "searching the videos is not available")
	default:
		return false
	}
	return true
}
