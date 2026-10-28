package filesapi_test

import (
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
	"github.com/zhongwater123/A-NAS/internal/filesapi"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceChangedBrokerErrorKeepsItsHTTPMeaning(t *testing.T) {
	w := httptest.NewRecorder()
	filesapi.WriteError(w, httptest.NewRequest("POST", "/api/v1/files/move", nil), slog.Default(), &wire.Error{Code: wire.CodeSourceChanged})
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"code":"source_changed"`) {
		t.Fatalf("response: %d %s", w.Code, w.Body.String())
	}
}
