package protocol

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/videocut/app"
	"github.com/zhongwater123/A-NAS/internal/videocut/domain"
)

func TestEditingErrorsAnswerWhatThePageActsOn(t *testing.T) {
	for _, c := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{&app.EditRefusal{Reason: "这个节拍已用在第 3 镜"}, http.StatusUnprocessableEntity, "edit_refused", "这个节拍已用在第 3 镜"},
		{fmt.Errorf("wrapped: %w", domain.ErrDraftConflict), http.StatusConflict, "draft_conflict", ""},
		{domain.ErrNotEditable, http.StatusConflict, "not_editable", ""},
		{domain.ErrAnalysisChanged, http.StatusConflict, "analysis_changed", "素材已按新模型重新分析，这一版不能再修改，可以用一句话重剪"},
		{domain.ErrSearchModel, http.StatusConflict, "search_model_changed", "分析模型已更新，重新分析后才能搜索"},
		{domain.ErrSearchUnavailable, http.StatusServiceUnavailable, "search_unavailable", ""},
		{domain.ErrNotFound, http.StatusNotFound, "not_found", ""},
	} {
		t.Run(c.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			(&handler{}).fail(w, httptest.NewRequest(http.MethodGet, "/", nil), c.err)
			var answer struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &answer); err != nil {
				t.Fatal(err, w.Body.String())
			}
			if w.Code != c.status || answer.Error.Code != c.code || c.message != "" && answer.Error.Message != c.message {
				t.Fatalf("%v answered %d %+v", c.err, w.Code, answer.Error)
			}
		})
	}
}

func TestASongIsSentInRanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio")
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Range", "bytes=2-5")
	w := httptest.NewRecorder()
	(&handler{}).serveSong(w, request, app.SongAudio{File: file, ContentType: "audio/mpeg"})
	if w.Code != http.StatusPartialContent || w.Body.String() != "2345" || w.Header().Get("Content-Range") != "bytes 2-5/10" ||
		w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("a range of the song = %d %q %v", w.Code, w.Body.String(), w.Header())
	}
}
