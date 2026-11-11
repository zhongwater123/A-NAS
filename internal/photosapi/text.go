package photosapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// The text read from a photo (docs/architecture/photo-ocr.md): reading it,
// correcting a field and taking a correction back, and the changes made.

type correctionRequest struct {
	Value *string `json:"value"`
}

type textChangesResponse struct {
	Items any `json:"items"`
}

func (h *handler) textRoutes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/text", h.photoText)
	h.mux.HandleFunc("PUT "+p+"/assets/{assetID}/text/fields/{field}", h.correctText)
	h.mux.HandleFunc("DELETE "+p+"/assets/{assetID}/text/fields/{field}", h.restoreText)
	h.mux.HandleFunc("GET "+p+"/assets/{assetID}/text/changes", h.textChanges)
	h.mux.HandleFunc("GET "+p+"/text-handles/{handle}", h.textHandle)
}

// textHandle gives a member the value a handle of their assistant stands
// for. The assistant's own socket does not route it (photoservice).
func (h *handler) textHandle(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	value, err := h.service.TextHandle(r.Context(), principal, r.PathValue("handle"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	// The value is a number of the member's: no cache keeps it.
	w.Header().Set("Cache-Control", "no-store")
	httpjson.Write(w, http.StatusOK, value)
}

func (h *handler) photoText(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	text, err := h.service.Text(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, text)
}

func (h *handler) correctText(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request correctionRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.Value == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "say what the field holds")
		return
	}
	text, err := h.service.CorrectText(r.Context(), principal, r.PathValue("assetID"), r.PathValue("field"), *request.Value)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, text)
}

func (h *handler) restoreText(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	text, err := h.service.RestoreText(r.Context(), principal, r.PathValue("assetID"), r.PathValue("field"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, text)
}

func (h *handler) textChanges(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	changes, err := h.service.TextChanges(r.Context(), principal, r.PathValue("assetID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, textChangesResponse{Items: changes})
}
