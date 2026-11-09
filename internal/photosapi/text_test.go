package photosapi_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

func TestPhotoTextOverHTTP(t *testing.T) {
	handler, _ := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	var photo photos.Asset
	if response := owner.upload(private.ID, "", "receipt.png", pngBytes(t, 70)); response.Code != http.StatusCreated {
		t.Fatalf("upload status = %d", response.Code)
	} else if err := json.Unmarshal(response.Body.Bytes(), &photo); err != nil {
		t.Fatal(err)
	}
	asset := "/api/v1/photos/assets/" + url.PathEscape(photo.ID)

	// Without local AI nothing is read, and nothing can be corrected.
	var text photos.PhotoText
	owner.json(http.MethodGet, asset+"/text", nil, http.StatusOK, &text)
	if text.Status != photos.TextNone || text.Fields == nil {
		t.Fatalf("text = %+v", text)
	}
	field := asset + "/text/fields/" + url.PathEscape("合计")
	owner.json(http.MethodPut, field, map[string]string{"value": "35.50"}, http.StatusConflict, nil)
	owner.json(http.MethodPut, field, map[string]string{}, http.StatusBadRequest, nil)
	owner.json(http.MethodDelete, field, nil, http.StatusConflict, nil)
	var changes struct {
		Items []photos.TextChange `json:"items"`
	}
	owner.json(http.MethodGet, asset+"/text/changes", nil, http.StatusOK, &changes)
	if changes.Items == nil || len(changes.Items) != 0 {
		t.Fatalf("changes = %+v", changes)
	}

	// Someone who cannot see the photo learns nothing of it.
	stranger := client{t: t, handler: handler, as: bob}
	for _, path := range []string{asset + "/text", asset + "/text/changes"} {
		stranger.json(http.MethodGet, path, nil, http.StatusNotFound, nil)
	}
	stranger.json(http.MethodPut, field, map[string]string{"value": "1"}, http.StatusNotFound, nil)
}
