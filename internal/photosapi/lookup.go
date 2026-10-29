package photosapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// LookupPath answers which of some photos the caller can see. The family
// service asks it with the reader's session to show a channel's photos
// (ADR 0029). Like ActivityPath it lies outside PathPrefix, so browsers
// never reach it.
const LookupPath = "/internal/photos/lookup"

// LookupRequest names the photos to look up.
type LookupRequest struct {
	IDs []string `json:"ids"`
}

// LookupResponse lists the photos the caller can see.
type LookupResponse struct {
	Items []photos.LookedUp `json:"items"`
}

func (h *handler) lookup(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request LookupRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	found, err := h.service.Lookup(r.Context(), principal, request.IDs)
	if errors.Is(err, photos.ErrTooManyIDs) {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, LookupResponse{Items: found})
}

// DecodeLookup reads a LookupPath response.
func DecodeLookup(body io.Reader) ([]photos.LookedUp, error) {
	var document LookupResponse
	if err := json.NewDecoder(io.LimitReader(body, 16<<20)).Decode(&document); err != nil {
		return nil, err
	}
	return document.Items, nil
}
