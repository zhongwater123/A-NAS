package httpjson_test

import (
	"bytes"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

func TestWriteAnswersJSONAndLogsAValueThatCannotBeEncoded(t *testing.T) {
	recorder := httptest.NewRecorder()
	httpjson.Write(recorder, http.StatusCreated, map[string]any{"name": "<家>"})
	if recorder.Code != http.StatusCreated || recorder.Header().Get("Content-Type") != "application/json" ||
		recorder.Body.String() != "{\"name\":\"\\u003c家\\u003e\"}\n" {
		t.Fatalf("Write() = %d %q %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	recorder = httptest.NewRecorder()
	httpjson.Write(recorder, http.StatusOK, map[string]float64{"ratio": math.NaN()})
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), `"code":"internal_error"`) {
		t.Fatalf("Write(NaN) = %d %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(logs.String(), "JSON response could not be encoded") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestWriteErrorUsesTheAPIsErrorShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	httpjson.WriteError(recorder, http.StatusNotFound, "not_found", "resource not found")
	if want := `{"error":{"code":"not_found","message":"resource not found"}}` + "\n"; recorder.Code != http.StatusNotFound || recorder.Body.String() != want {
		t.Fatalf("WriteError() = %d %q", recorder.Code, recorder.Body.String())
	}
}

func TestDecodeAcceptsOneJSONValueOfTheExpectedShape(t *testing.T) {
	type request struct {
		Name string `json:"name"`
	}
	tests := []struct {
		name, contentType, body string
		want                    error
	}{
		{name: "object", contentType: "application/json", body: `{"name":"a"}`},
		{name: "charset and space after", contentType: "Application/JSON; charset=utf-8", body: "{\"name\":\"a\"}\n \n"},
		{name: "no content type", contentType: "", body: `{"name":"a"}`, want: httpjson.ErrContentType},
		{name: "form", contentType: "application/x-www-form-urlencoded", body: `name=a`, want: httpjson.ErrContentType},
		{name: "malformed content type", contentType: "application/json; =", body: `{"name":"a"}`, want: httpjson.ErrContentType},
		{name: "empty", contentType: "application/json", body: ``, want: httpjson.ErrInvalid},
		{name: "unknown field", contentType: "application/json", body: `{"name":"a","admin":true}`, want: httpjson.ErrInvalid},
		{name: "second value", contentType: "application/json", body: `{"name":"a"}{"name":"b"}`, want: httpjson.ErrInvalid},
		{name: "trailing data", contentType: "application/json", body: `{"name":"a"} x`, want: httpjson.ErrInvalid},
		{name: "too large", contentType: "application/json", body: `{"name":"` + strings.Repeat("a", 64) + `"}`, want: httpjson.ErrTooLarge},
		{name: "too large after the value", contentType: "application/json", body: `{"name":"a"}` + strings.Repeat(" ", 64), want: httpjson.ErrTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			if test.contentType != "" {
				r.Header.Set("Content-Type", test.contentType)
			}
			var got request
			err := httpjson.Decode(httptest.NewRecorder(), r, 40, &got)
			if !errors.Is(err, test.want) || (test.want == nil && got.Name != "a") {
				t.Fatalf("Decode() = %+v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestReadAnswersARefusedBody(t *testing.T) {
	for body, want := range map[string]int{`{"name":"a"}`: http.StatusOK, `{"name":`: http.StatusBadRequest, strings.Repeat(" ", 64): http.StatusRequestEntityTooLarge} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		var target struct {
			Name string `json:"name"`
		}
		if httpjson.Read(recorder, r, 32, &target) {
			recorder.WriteHeader(http.StatusOK)
		}
		if recorder.Code != want {
			t.Errorf("Read(%q) = %d %s, want %d", body, recorder.Code, recorder.Body.String(), want)
		}
	}
}
