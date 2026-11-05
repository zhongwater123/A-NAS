package webentry_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/webentry"
)

func TestInternetHandlerMarksRequestsForItsDomain(t *testing.T) {
	var seen string
	marked := false
	handler := webentry.InternetHandler("nas.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, marked = webentry.Internet(r)
	}))
	for _, host := range []string{"nas.example.com", "NAS.example.com", "nas.example.com:443"} {
		seen, marked = "", false
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.Host = host
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || !marked || seen != "nas.example.com" {
			t.Fatalf("Host %q: status %d, marked %v as %q", host, recorder.Code, marked, seen)
		}
	}
}

func TestInternetHandlerRefusesOtherHosts(t *testing.T) {
	handler := webentry.InternetHandler("nas.example.com", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("a request for another host reached the handler")
	}))
	for _, host := range []string{"172.18.45.48", "127.0.0.1:8081", "localhost", "attacker.example", "nas.example.com.attacker.example", ""} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.Host = host
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusMisdirectedRequest {
			t.Fatalf("Host %q: status %d, want 421", host, recorder.Code)
		}
	}
}

func TestRequestsOutsideTheInternetEntryAreNotMarked(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	if host, ok := webentry.Internet(request); ok {
		t.Fatalf("Internet() = %q, true for a request on the main listener", host)
	}
}
