package montageapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/montage"
)

func TestEditingErrorsAnswerWhatThePageActsOn(t *testing.T) {
	for _, c := range []struct {
		err     error
		status  int
		code    string
		message string
	}{
		{&montage.EditRefusal{Reason: "这个节拍已用在第 3 镜"}, http.StatusUnprocessableEntity, "edit_refused", "这个节拍已用在第 3 镜"},
		{fmt.Errorf("wrapped: %w", montage.ErrDraftConflict), http.StatusConflict, "draft_conflict", ""},
		{montage.ErrNotEditable, http.StatusConflict, "not_editable", ""},
		{montage.ErrSearchModel, http.StatusConflict, "search_model_changed", "分析模型已更新，重新分析后才能搜索"},
		{montage.ErrSearchUnavailable, http.StatusServiceUnavailable, "search_unavailable", ""},
		{montage.ErrNotFound, http.StatusNotFound, "not_found", ""},
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
