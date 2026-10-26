package photosapi_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"os"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

func TestOversizedLegacyPhotoViewReturnsTooLargeAndKeepsOriginal(t *testing.T) {
	handler, service := newAPI(t)
	owner := client{t: t, handler: handler, as: alice}
	private, _ := librariesOf(owner)
	original := pngBytes(t, 40)
	response := owner.upload(private.ID, "", "legacy.png", original)
	if response.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	var asset photos.Asset
	if err := json.Unmarshal(response.Body.Bytes(), &asset); err != nil {
		t.Fatal(err)
	}
	stored, err := service.Open(t.Context(), alice, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := stored.Reader.Name()
	_ = stored.Reader.Close()
	// Simulate an original accepted before the budget was introduced. Only
	// the test's temporary object is replaced, using a header without pixels.
	header := bytes.Clone(original[:33])
	binary.BigEndian.PutUint32(header[16:20], 16384)
	binary.BigEndian.PutUint32(header[20:24], 16384)
	header[24], header[25] = 16, 6
	binary.BigEndian.PutUint32(header[29:33], crc32.ChecksumIEEE(header[12:29]))
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, header, 0o400); err != nil {
		t.Fatal(err)
	}
	assistant := client{t: t, handler: handler, as: photosapi.NewAssistantPrincipal(alice.UserID, "alice")}
	for _, caller := range []client{owner, assistant} {
		response = caller.do(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/view", nil, "")
		if response.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(response.Body.Bytes(), []byte(`"too_large"`)) {
			t.Fatalf("view: %d %s; want 413 too_large", response.Code, response.Body.String())
		}
	}
	response = owner.do(http.MethodGet, "/api/v1/photos/assets/"+asset.ID+"/original", nil, "")
	if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), header) {
		t.Fatalf("original must remain readable: %d", response.Code)
	}
	if worked, err := service.ProcessMediaJob(t.Context()); !worked || err != nil {
		t.Fatalf("media job: %v, %v", worked, err)
	}
	if got, err := service.Get(t.Context(), alice, asset.ID); err != nil || got.Thumbnail != photos.ThumbnailFailed {
		t.Fatalf("oversized thumbnail state = %q, %v; want permanent failure", got.Thumbnail, err)
	}
	if recovery, err := service.RecoverJobs(t.Context()); err != nil || recovery.Requeued != 0 {
		t.Fatalf("recovery retried an oversized original: %+v, %v", recovery, err)
	}
	if worked, err := service.ProcessMediaJob(t.Context()); worked || err != nil {
		t.Fatalf("oversized job retried: %v, %v", worked, err)
	}
}
