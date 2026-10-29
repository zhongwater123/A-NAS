package photosapi

import (
	"bytes"
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// People and pets (docs/architecture/photo-people.md): each library's people
// with their photos, what members change about them, and the avatars of the
// faces and pets in a photo. Face vectors never leave the photo service.

type personChangeRequest struct {
	Name    *string `json:"name"`
	Hidden  *bool   `json:"hidden"`
	CoverID *string `json:"coverId"`
}

type mergeRequest struct {
	Into string `json:"into"`
}

type librarySettingsRequest struct {
	People *bool `json:"people"`
}

type assignRequest struct {
	// PersonID names an existing person; with none, Name founds a new one.
	PersonID string `json:"personId"`
	Name     string `json:"name"`
}

func (h *handler) peopleRoutes() {
	p := PathPrefix
	h.mux.HandleFunc("GET "+p+"/people", h.listPeople)
	h.mux.HandleFunc("GET "+p+"/people/{personID}/assets", h.personPhotos)
	h.mux.HandleFunc("PATCH "+p+"/people/{personID}", h.changePerson)
	h.mux.HandleFunc("POST "+p+"/people/{personID}/merge", h.mergePeople)
	h.mux.HandleFunc("DELETE "+p+"/people/{personID}/assets/{assetID}", h.removeFromPerson)
	h.mux.HandleFunc("PUT "+p+"/assets/{assetID}/people/{occurrenceID}", h.assignOccurrence)
	h.mux.HandleFunc("GET "+p+"/occurrences/{occurrenceID}/avatar", h.occurrenceAvatar)
	h.mux.HandleFunc("PATCH "+p+"/libraries/{libraryID}", h.changeLibrary)
}

// changeLibrary turns a library's people and pets on or off; off clears
// what was recognised there and keeps the names.
func (h *handler) changeLibrary(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request librarySettingsRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	if request.People == nil {
		httpjson.WriteError(w, http.StatusBadRequest, "invalid_request", "say whether the library recognises people and pets")
		return
	}
	library, err := h.service.SetPeople(r.Context(), principal, r.PathValue("libraryID"), *request.People)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, library)
}

func (h *handler) listPeople(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	query := r.URL.Query()
	people, err := h.service.People(r.Context(), principal, query.Get("viewing"), query.Get("hidden") == "1")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, itemsResponse[photos.Person]{Items: people})
}

func (h *handler) personPhotos(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	page, err := h.service.PersonPhotos(r.Context(), principal, r.PathValue("personID"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, timelineResponse{Items: page.Assets, Next: page.Next})
}

func (h *handler) changePerson(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request personChangeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	person, err := h.service.UpdatePerson(r.Context(), principal, r.PathValue("personID"),
		photos.PersonChange{Name: request.Name, Hidden: request.Hidden, CoverID: request.CoverID})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, person)
}

func (h *handler) mergePeople(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request mergeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	person, err := h.service.MergePeople(r.Context(), principal, r.PathValue("personID"), request.Into)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, person)
}

func (h *handler) removeFromPerson(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	if err := h.service.RemoveFromPerson(r.Context(), principal, r.PathValue("personID"), r.PathValue("assetID")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) assignOccurrence(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	var request assignRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	asset, err := h.service.AssignOccurrence(r.Context(), principal, r.PathValue("assetID"), r.PathValue("occurrenceID"),
		request.PersonID, request.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, asset)
}

// occurrenceAvatar serves the square crop of one face or pet. An occurrence's
// ID changes whenever its photo is scanned again, so the ID is its ETag.
func (h *handler) occurrenceAvatar(w http.ResponseWriter, r *http.Request) {
	principal, _ := principalFrom(r)
	id := r.PathValue("occurrenceID")
	avatar, err := h.service.OccurrenceAvatar(r.Context(), principal, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", `"`+id+`"`)
	http.ServeContent(w, r, "avatar.jpg", time.Time{}, bytes.NewReader(avatar))
}
