package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/familyapi"
	"github.com/zhongwater123/A-NAS/internal/familyservice"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// decode reads a JSON answer into target.
func decode(t *testing.T, body *bytes.Buffer, target any) {
	t.Helper()
	if err := json.Unmarshal(body.Bytes(), target); err != nil {
		t.Fatalf("decode %s: %v", body.String(), err)
	}
}

// familyForm is a multipart body with text fields, then one file part.
func familyForm(t *testing.T, fields map[string]string, name, contentType string, content []byte) (string, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, key := range []string{"requestKey", "title", "occurredOn"} {
		if value, ok := fields[key]; ok {
			_ = writer.WriteField(key, value)
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.String(), writer.FormDataContentType()
}

// TestFamilyCenterKeepsToTheContract drives 家庭中心 through the Product
// Service as development runs it: the family store and the photo service in
// process, each confirming the session itself.
func TestFamilyCenterKeepsToTheContract(t *testing.T) {
	dir := t.TempDir()
	controlDB := filepath.Join(dir, "control.db")
	store, err := accounts.OpenSQLite(controlDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	users := accounts.NewService(store, apiCredentials{}, accounts.Options{})
	photoService, err := photos.Open(filepath.Join(dir, "photos"), photos.Options{DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = photoService.Close() })
	photoAPI := localPhotos(t, photoService, sessionDirectory(t, controlDB))
	familyDir := filepath.Join(dir, "family")
	if err := os.Mkdir(familyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	familyStore, err := family.Open(familyDir, family.Options{
		Accounts: familyservice.Accounts(func(ctx context.Context) ([]accounts.Profile, error) {
			active, err := users.ActiveUsers(ctx)
			if err != nil {
				return nil, err
			}
			profiles := []accounts.Profile{}
			for _, user := range active {
				profiles = append(profiles, accounts.Profile{ID: user.ID, Username: user.Username})
			}
			return profiles, nil
		}),
		Photos: familyservice.Photos{API: photoAPI},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = familyStore.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := checkedProduct(t, httpapi.ProductDependencies{
		Reader: fake.NewHealthy(), DataSource: httpapi.DataSourceSimulated, Accounts: users,
		Photos: photoAPI, Family: familyapi.New(familyStore, logger, nil), Logger: logger,
	})
	f := &activityFixture{t: t, handler: handler, client: &contractClient{t: t, handler: handler}}

	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"alice","password":"alice password for testing"}`, nil, http.StatusCreated)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"bob","password":"bob password for testing"}`, nil, http.StatusCreated)
	alice := f.signIn("/api/v1/session", "alice", "alice password for testing", "172.18.45.20", http.StatusOK)
	bob := f.signIn("/api/v1/session", "bob", "bob password for testing", "172.18.45.21", http.StatusOK)

	// Joining: the owner founds the family, alice asks and is let in.
	owner.do(http.MethodGet, "/api/v1/family", "", nil, http.StatusOK)
	owner.do(http.MethodGet, "/api/v1/family/catalog", "", nil, http.StatusOK)
	owner.do(http.MethodPost, "/api/v1/family/join", `{"role":"妈妈"}`, nil, http.StatusOK)
	alice.do(http.MethodPost, "/api/v1/family/join", `{"role":"外婆"}`, nil, http.StatusOK)
	alice.do(http.MethodGet, "/api/v1/family/hints", "", nil, http.StatusOK)
	var aliceID, bobID string
	var overview family.Overview
	decode(t, owner.do(http.MethodGet, "/api/v1/family", "", nil, http.StatusOK).Body, &overview)
	if len(overview.Requests) != 1 {
		t.Fatalf("requests = %+v", overview.Requests)
	}
	aliceID = overview.Requests[0].AccountID
	owner.do(http.MethodPost, "/api/v1/family/requests/"+aliceID+"/approve", "", nil, http.StatusOK)
	bob.do(http.MethodPost, "/api/v1/family/join", `{"role":"舅舅"}`, nil, http.StatusOK)
	bob.do(http.MethodDelete, "/api/v1/family/join", "", nil, http.StatusNoContent)
	bob.do(http.MethodPost, "/api/v1/family/join", `{"role":"舅舅"}`, nil, http.StatusOK)
	decode(t, owner.do(http.MethodGet, "/api/v1/family", "", nil, http.StatusOK).Body, &overview)
	bobID = overview.Requests[0].AccountID
	owner.do(http.MethodDelete, "/api/v1/family/requests/"+bobID, "", nil, http.StatusNoContent)
	bob.do(http.MethodGet, "/api/v1/family/babies/none/records", "", nil, http.StatusForbidden)
	alice.do(http.MethodPatch, "/api/v1/family/me", `{"role":"姥姥"}`, nil, http.StatusOK)
	owner.do(http.MethodPatch, "/api/v1/family/members/"+aliceID, `{"guardian":true}`, nil, http.StatusOK)
	owner.do(http.MethodPatch, "/api/v1/family/members/"+aliceID, `{"guardian":false}`, nil, http.StatusOK)
	owner.do(http.MethodPost, "/api/v1/family/leave", "", nil, http.StatusConflict)

	// A baby and the profile.
	var baby family.Baby
	decode(t, owner.do(http.MethodPost, "/api/v1/family/babies", `{"name":"小米粒","sex":"female","birthDate":"2026-03-15"}`, nil, http.StatusCreated).Body, &baby)
	alice.do(http.MethodPost, "/api/v1/family/babies", `{"name":"二宝","sex":"male","birthDate":"2026-03-15"}`, nil, http.StatusForbidden)
	babyPath := "/api/v1/family/babies/" + baby.ID
	alice.do(http.MethodGet, babyPath, "", nil, http.StatusOK)
	owner.do(http.MethodPatch, babyPath, `{"name":"小米粒","nickname":"米米","sex":"female","birthDate":"2026-03-15","version":1}`, nil, http.StatusOK)
	owner.do(http.MethodPatch, babyPath, `{"name":"小米粒","sex":"female","birthDate":"2026-03-15","version":1}`, nil, http.StatusConflict)
	body, contentType := multipartFile(t, "avatar.png", tinyPNG(t))
	owner.do(http.MethodPut, babyPath+"/avatar", body, map[string]string{"Content-Type": contentType}, http.StatusOK)
	alice.do(http.MethodGet, babyPath+"/avatar", "", nil, http.StatusOK)
	owner.do(http.MethodDelete, babyPath+"/avatar", "", nil, http.StatusOK)
	alice.do(http.MethodGet, babyPath+"/avatar", "", nil, http.StatusNotFound)

	// Photos go into the shared library as the uploader, then into a record.
	var libraries struct {
		Items []photos.Library `json:"items"`
	}
	decode(t, alice.do(http.MethodGet, "/api/v1/photos/libraries", "", nil, http.StatusOK).Body, &libraries)
	var shared string
	for _, library := range libraries.Items {
		if library.Kind == photos.LibraryKindShared {
			shared = library.ID
		}
	}
	body, contentType = multipartFile(t, "park.png", tinyPNG(t))
	var asset photos.Asset
	decode(t, alice.do(http.MethodPost, "/api/v1/photos/libraries/"+shared+"/uploads", body, map[string]string{"Content-Type": contentType}, http.StatusCreated).Body, &asset)
	var record family.Record
	decode(t, alice.do(http.MethodPost, babyPath+"/records", `{"requestKey":"r1","kind":"photos","text":"公园","photos":["`+asset.ID+`"]}`, nil, http.StatusCreated).Body, &record)
	if record.AuthorID != aliceID || len(record.Photos) != 1 {
		t.Fatalf("photo record = %+v", record)
	}
	alice.do(http.MethodPost, babyPath+"/records", `{"kind":"photos","photos":["missing"]}`, nil, http.StatusUnprocessableEntity)
	recordPath := "/api/v1/family/records/" + record.ID
	owner.do(http.MethodGet, babyPath+"/records", "", nil, http.StatusOK)
	owner.do(http.MethodGet, babyPath+"/records?kind=photos", "", nil, http.StatusOK)
	owner.do(http.MethodGet, babyPath+"/album", "", nil, http.StatusOK)
	owner.do(http.MethodGet, recordPath, "", nil, http.StatusOK)
	owner.do(http.MethodPatch, recordPath, `{"text":"改","version":1}`, nil, http.StatusForbidden)
	alice.do(http.MethodPatch, recordPath, `{"text":"公园散步","version":1}`, nil, http.StatusOK)
	var comment family.Comment
	decode(t, owner.do(http.MethodPost, recordPath+"/comments", `{"text":"真可爱"}`, nil, http.StatusCreated).Body, &comment)
	alice.do(http.MethodDelete, "/api/v1/family/comments/"+comment.ID, "", nil, http.StatusForbidden)
	owner.do(http.MethodDelete, "/api/v1/family/comments/"+comment.ID, "", nil, http.StatusNoContent)
	owner.do(http.MethodDelete, recordPath, "", nil, http.StatusNoContent)
	alice.do(http.MethodGet, babyPath+"/trash", "", nil, http.StatusOK)
	alice.do(http.MethodPost, recordPath+"/restore", "", nil, http.StatusOK)

	// Growth, vaccines, milestones and the message wall.
	alice.do(http.MethodPost, babyPath+"/records", `{"kind":"measurement","occurredOn":"2026-09-15","measurement":{"heightCm":66,"weightKg":7.2,"headCm":42.5}}`, nil, http.StatusCreated)
	alice.do(http.MethodGet, babyPath+"/growth", "", nil, http.StatusOK)
	owner.do(http.MethodPost, babyPath+"/records", `{"kind":"vaccine","occurredOn":"2026-05-16","vaccine":{"code":"dtap-ipv-hib","dose":1,"clinic":"社区医院"}}`, nil, http.StatusCreated)
	owner.do(http.MethodPut, babyPath+"/vaccine-plan", `{"codes":["pcv13"]}`, nil, http.StatusOK)
	owner.do(http.MethodPut, babyPath+"/vaccine-plan", `{"codes":["dtap"]}`, nil, http.StatusUnprocessableEntity)
	alice.do(http.MethodGet, babyPath+"/vaccines", "", nil, http.StatusOK)
	owner.do(http.MethodPost, babyPath+"/records", `{"kind":"milestone","milestone":{"title":"","autoKey":"day-100"},"text":"百天"}`, nil, http.StatusCreated)
	alice.do(http.MethodGet, babyPath+"/milestones", "", nil, http.StatusOK)
	alice.do(http.MethodPost, babyPath+"/wall", `{"text":"外婆想你了"}`, nil, http.StatusCreated)
	owner.do(http.MethodGet, babyPath+"/wall", "", nil, http.StatusOK)

	// Recordings stream in and play back with Range.
	body, contentType = familyForm(t, map[string]string{"title": "儿歌", "requestKey": "a1"}, "song.mp3", "audio/mpeg", silentMP3("儿歌", 20))
	var recording family.Record
	decode(t, alice.do(http.MethodPost, babyPath+"/recordings", body, map[string]string{"Content-Type": contentType}, http.StatusCreated).Body, &recording)
	alice.do(http.MethodGet, "/api/v1/family/records/"+recording.ID+"/audio", "", nil, http.StatusOK)
	alice.do(http.MethodGet, "/api/v1/family/records/"+recording.ID+"/audio", "", map[string]string{"Range": "bytes=0-9"}, http.StatusPartialContent)
	body, contentType = familyForm(t, nil, "clip.mp4", "video/mp4", []byte("not audio"))
	alice.do(http.MethodPost, babyPath+"/recordings", body, map[string]string{"Content-Type": contentType}, http.StatusUnprocessableEntity)

	// The desktop tells each member once.
	var hints struct {
		Items []family.Hint `json:"items"`
	}
	decode(t, alice.do(http.MethodGet, "/api/v1/family/hints", "", nil, http.StatusOK).Body, &hints)
	if len(hints.Items) == 0 {
		t.Fatal("a seven-month-old has doses due")
	}
	keys, _ := json.Marshal(map[string][]string{"keys": {hints.Items[0].Key}})
	alice.do(http.MethodPost, "/api/v1/family/hints/seen", string(keys), nil, http.StatusNoContent)

	// Removing a member, deleting a baby and bringing it back.
	bob.do(http.MethodPost, "/api/v1/family/join", `{"role":"舅舅"}`, nil, http.StatusOK)
	owner.do(http.MethodPost, "/api/v1/family/requests/"+bobID+"/approve", "", nil, http.StatusOK)
	bob.do(http.MethodPost, "/api/v1/family/leave", "", nil, http.StatusNoContent)
	owner.do(http.MethodDelete, "/api/v1/family/members/"+aliceID, "", nil, http.StatusNoContent)
	alice.do(http.MethodGet, babyPath+"/records", "", nil, http.StatusForbidden)
	owner.do(http.MethodDelete, babyPath+"?name=%E5%B0%8F%E7%B1%B3", "", nil, http.StatusUnprocessableEntity)
	owner.do(http.MethodDelete, babyPath+"?name=%E5%B0%8F%E7%B1%B3%E7%B2%92", "", nil, http.StatusNoContent)
	owner.do(http.MethodGet, babyPath, "", nil, http.StatusNotFound)
	owner.do(http.MethodPost, babyPath+"/restore", "", nil, http.StatusOK)
}
