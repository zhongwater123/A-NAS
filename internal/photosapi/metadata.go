package photosapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// Albums, user tags and AI corrections, which take a photo out of an AI
// cluster (docs/architecture/photo-ai.md).

type nameRequest struct {
	Name string `json:"name"`
}

type albumMemberRequest struct {
	AssetID string `json:"assetId"`
}

func (h *handler) metadataRoutes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p+"/libraries/{libraryID}/albums", h.listAlbums)
	h.mux.HandleFunc("POST "+p+"/libraries/{libraryID}/albums", h.createAlbum)
	h.mux.HandleFunc("PATCH "+p+"/albums/{albumID}", h.renameAlbum)
	h.mux.HandleFunc("DELETE "+p+"/albums/{albumID}", h.deleteAlbum)
	h.mux.HandleFunc("GET "+p+"/albums/{albumID}/assets", h.albumPhotos)
	h.mux.HandleFunc("POST "+p+"/albums/{albumID}/assets", h.addToAlbum)
	h.mux.HandleFunc("DELETE "+p+"/albums/{albumID}/assets/{assetID}", h.removeFromAlbum)
	h.mux.HandleFunc("POST "+p+"/assets/{assetID}/tags", h.addTag)
	h.mux.HandleFunc("DELETE "+p+"/assets/{assetID}/tags/{name}", h.removeTag)
	h.mux.HandleFunc("DELETE "+p+"/assets/{assetID}/ai-labels/{labelID}", h.hideAILabel)
}

func (h *handler) listAlbums(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	albums, err := h.service.Albums(r.Context(), principal, r.PathValue("libraryID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsResponse[photos.Album]{Items: albums})
}

func (h *handler) createAlbum(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request nameRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	album, err := h.service.CreateAlbum(r.Context(), principal, r.PathValue("libraryID"), request.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, album)
}

func (h *handler) renameAlbum(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request nameRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	album, err := h.service.RenameAlbum(r.Context(), principal, r.PathValue("albumID"), request.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, album)
}

func (h *handler) deleteAlbum(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	if err := h.service.DeleteAlbum(r.Context(), principal, r.PathValue("albumID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) albumPhotos(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	page, err := h.service.AlbumPhotos(r.Context(), principal, r.PathValue("albumID"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
}

func (h *handler) addToAlbum(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request albumMemberRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	asset, err := h.service.AddToAlbum(r.Context(), principal, r.PathValue("albumID"), request.AssetID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (h *handler) removeFromAlbum(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	if err := h.service.RemoveFromAlbum(r.Context(), principal, r.PathValue("albumID"), r.PathValue("assetID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) addTag(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request nameRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	asset, err := h.service.AddTag(r.Context(), principal, r.PathValue("assetID"), request.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (h *handler) removeTag(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.RemoveTag(r.Context(), principal, r.PathValue("assetID"), r.PathValue("name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}

func (h *handler) hideAILabel(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	asset, err := h.service.HideAILabel(r.Context(), principal, r.PathValue("assetID"), r.PathValue("labelID"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, asset)
}
