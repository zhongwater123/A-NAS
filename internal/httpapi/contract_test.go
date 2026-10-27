package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"

	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/openapitest"
)

// Subtitles are WebVTT, which kin-openapi does not decode by itself.
func init() { openapi3filter.RegisterBodyDecoder("text/vtt", openapi3filter.FileBodyDecoder) }

// checkedProduct is the product API whose every exchange is checked against
// the specification.
func checkedProduct(t *testing.T, dependencies httpapi.ProductDependencies) http.Handler {
	t.Helper()
	return contractChecked(t, httpapi.NewProduct(dependencies))
}

// contractChecked validates every exchange against the OpenAPI document: an
// exchange on no specified operation fails, as with openapitest.Check,
// unless the answer says there is no such operation (404 not_found or 405
// method_not_allowed); responses always, with any status the operation does not declare failing,
// and requests when the server accepted them. The guard's answers for a
// missing session, CSRF token, new password or administrator are the same
// for every operation and documented by its security and the CSRFToken
// parameter, so they are not looked up per operation. WebSocket upgrades
// pass through unchecked. Each checked status is recorded for the report in
// TestMain.
func contractChecked(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	router := openapitest.Router(t)
	ctx := context.Background()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		recorder := httptest.NewRecorder()
		next.ServeHTTP(recorder, r)
		request := httptest.NewRequest(r.Method, "http://127.0.0.1:8080"+r.URL.RequestURI(), bytes.NewReader(body))
		request.Header = r.Header.Clone()
		route, params, err := router.FindRoute(request)
		if err != nil {
			if code := answerCode(recorder); !(recorder.Code == http.StatusNotFound && code == "not_found") &&
				!(recorder.Code == http.StatusMethodNotAllowed && code == "method_not_allowed") {
				t.Errorf("find OpenAPI operation for %s %s (answered %d %s): %v", r.Method, r.URL.Path, recorder.Code, code, err)
			}
		} else if !guardAnswer(recorder) {
			input := &openapi3filter.RequestValidationInput{Request: request, PathParams: params, Route: route, Options: &openapi3filter.Options{
				AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil },
			}}
			if recorder.Code < 300 {
				if err := openapi3filter.ValidateRequest(ctx, input); err != nil {
					t.Errorf("validate request %s %s: %v", r.Method, r.URL.Path, err)
				}
			}
			response := &openapi3filter.ResponseValidationInput{
				RequestValidationInput: input, Status: recorder.Code, Header: recorder.Header(),
				Options: &openapi3filter.Options{IncludeResponseStatus: true},
			}
			response.SetBodyBytes(recorder.Body.Bytes())
			if err := openapi3filter.ValidateResponse(ctx, response); err != nil {
				t.Errorf("validate response %s %s: %d %v", r.Method, r.URL.Path, recorder.Code, err)
			} else {
				openapitest.Record(route, recorder.Code)
			}
		}
		maps.Copy(w.Header(), recorder.Header())
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	})
}

// answerCode is the code of an error answer, empty for any other.
func answerCode(recorder *httptest.ResponseRecorder) string {
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return body.Error.Code
}

// guardAnswer reports whether the product's guard answered instead of the
// operation.
func guardAnswer(recorder *httptest.ResponseRecorder) bool {
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(recorder.Body.Bytes(), &body) != nil {
		return false
	}
	switch status, code := recorder.Code, body.Error.Code; {
	case status == http.StatusUnauthorized && code == "authentication_required":
		return true
	case status == http.StatusForbidden && (code == "csrf_failed" || code == "password_change_required"):
		return true
	case status == http.StatusForbidden && code == "forbidden" && body.Error.Message == "administrator permission is required":
		return true
	}
	return false
}
