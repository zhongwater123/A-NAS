package httpapi_test

import (
	"net/http"
	"regexp"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

// TestEinkScreensKeepToTheContract pairs a screen as an administrator and
// asks as the screen with its device token, through the Product Service as
// development runs it (docs/specs/eink-screens.md).
func TestEinkScreensKeepToTheContract(t *testing.T) {
	f := newActivityFixture(t, func(*photos.Service, *accounts.Service) http.Handler { return nil })
	owner := f.signIn("/api/v1/setup/admin", "owner", "correct horse battery staple", "", http.StatusCreated)
	owner.do(http.MethodPost, "/api/v1/users", `{"username":"bob","password":"bob password for testing"}`, nil, http.StatusCreated)
	bob := f.signIn("/api/v1/session", "bob", "bob password for testing", "172.18.45.21", http.StatusOK)

	var paired struct {
		Screen family.Screen `json:"screen"`
		Token  string        `json:"token"`
	}
	decode(t, owner.do(http.MethodPost, "/api/v1/eink/screens", `{"name":"客厅"}`, nil, http.StatusCreated).Body, &paired)
	if paired.Screen.Name != "客厅" || len(paired.Token) < 40 {
		t.Fatalf("paired = %+v", paired)
	}
	owner.do(http.MethodPost, "/api/v1/eink/screens", `{"name":"客厅"}`, nil, http.StatusConflict)
	owner.do(http.MethodPost, "/api/v1/eink/screens", `{"name":"   "}`, nil, http.StatusUnprocessableEntity)
	bob.do(http.MethodPost, "/api/v1/eink/screens", `{"name":"玄关"}`, nil, http.StatusForbidden)
	bob.do(http.MethodGet, "/api/v1/eink/screens", "", nil, http.StatusForbidden)
	screenPath := "/api/v1/eink/screens/" + paired.Screen.ID
	owner.do(http.MethodPatch, screenPath, `{"name":"客厅屏"}`, nil, http.StatusOK)
	owner.do(http.MethodPatch, "/api/v1/eink/screens/scr_missing", `{"name":"书房"}`, nil, http.StatusNotFound)

	// The screen asks: no cookie, its token as a bearer token.
	screen := &contractClient{t: t, handler: f.handler, forwardedFor: "172.18.45.60"}
	bearer := map[string]string{"Authorization": "Bearer " + paired.Token}
	var answer struct {
		Action     string `json:"action"`
		Reason     string `json:"reason"`
		Key        string `json:"key"`
		Bin        string `json:"bin"`
		Bar        string `json:"bar"`
		ServerTime string `json:"server_time"`
		NextAt     string `json:"next_at"`
	}
	decode(t, screen.do(http.MethodGet, "/api/v1/eink/next?fw=0.2.5-s3&bat=35&usb=0&hist=0712a-none-4", "", bearer, http.StatusOK).Body, &answer)
	stamp := regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d$`)
	if answer.Action != "full" || answer.Reason != "开机" || !regexp.MustCompile(`^[0-9a-f]{16}:bw$`).MatchString(answer.Key) ||
		answer.Bin != "/api/v1/eink/screen.bin?h="+answer.Key[:16] || answer.Bar != "/api/v1/eink/screen-bar.bin" ||
		!stamp.MatchString(answer.ServerTime) || !stamp.MatchString(answer.NextAt) {
		t.Fatalf("first answer = %+v", answer)
	}
	key := answer.Key
	answer.Bin = "" // absent when nothing is to be drawn
	decode(t, screen.do(http.MethodGet, "/api/v1/eink/next?shown="+key, "", bearer, http.StatusOK).Body, &answer)
	if answer.Action != "none" || answer.Bin != "" {
		t.Fatalf("nothing changed, yet the answer is %+v", answer)
	}
	// Until the NAS draws, the picture is not available and the screen keeps
	// what it shows.
	screen.do(http.MethodGet, "/api/v1/eink/screen.bin?h="+key[:16], "", bearer, http.StatusServiceUnavailable)
	screen.do(http.MethodGet, "/api/v1/eink/screen-bar.bin", "", bearer, http.StatusServiceUnavailable)

	var list struct {
		Items []family.Screen `json:"items"`
	}
	decode(t, owner.do(http.MethodGet, "/api/v1/eink/screens", "", nil, http.StatusOK).Body, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "客厅屏" || list.Items[0].SeenAt == nil || list.Items[0].Report == nil ||
		*list.Items[0].Report.Battery != 35 || list.Items[0].Report.Firmware != "0.2.5-s3" {
		t.Fatalf("screens = %+v", list.Items)
	}

	// A browser never asks as a screen; a wrong token is refused like none.
	screen.do(http.MethodGet, "/api/v1/eink/next", "", map[string]string{"Authorization": "Bearer " + paired.Token, "Origin": "http://172.18.45.10"}, http.StatusForbidden)
	screen.do(http.MethodGet, "/api/v1/eink/next", "", map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized)
	screen.do(http.MethodGet, "/api/v1/eink/next", "", nil, http.StatusUnauthorized)
	// A session is no device token.
	owner.do(http.MethodGet, "/api/v1/eink/next", "", nil, http.StatusUnauthorized)

	owner.do(http.MethodDelete, screenPath, "", nil, http.StatusNoContent)
	owner.do(http.MethodDelete, screenPath, "", nil, http.StatusNotFound)
	screen.do(http.MethodGet, "/api/v1/eink/next", "", bearer, http.StatusUnauthorized)
}
