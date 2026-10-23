package assistantapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func (h *harness) request(method, path, body string) (int, string) {
	h.t.Helper()
	request, err := http.NewRequest(method, h.server.URL+PathPrefix+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response.StatusCode, strings.TrimSuffix(string(data), "\n")
}

func TestTheMemoryPageReadsAndForgetsWhatTheAssistantLearned(t *testing.T) {
	h := newHarness(t)
	status, body := h.request(http.MethodGet, "/memory", "")
	var page learned
	if status != http.StatusOK || json.Unmarshal([]byte(body), &page) != nil {
		t.Fatalf("memory = %d %s", status, body)
	}
	if len(page.Memories) != 2 || page.Memories[0].Text != "alice 喜欢简短的回答" || page.Memories[1].Store != "profile" {
		t.Fatalf("memories = %+v", page.Memories)
	}
	// Every skill the assistant made is the member's, its name notwithstanding: the
	// review named one anas-photos on the Experimental NAS. A-NAS's own skills are
	// not in Hermes's graph, so not the member's to see or delete here.
	if len(page.Skills) != 2 || page.Skills[0].ID != "family-trip" || page.Skills[0].CreatedBy != "learn" ||
		page.Skills[1].ID != "anas-photos" || page.Skills[1].CreatedBy != "agent" {
		t.Fatalf("skills = %+v", page.Skills)
	}
	if status, body := h.request(http.MethodGet, "/memory/items/family-trip", ""); status != http.StatusOK || !strings.Contains(body, "出游计划") {
		t.Fatalf("skill = %d %s", status, body)
	}
	for _, id := range []string{"anas-files", "../config", "nothing-like-this"} {
		if status, _ := h.request(http.MethodGet, "/memory/items/"+url.PathEscape(id), ""); status == http.StatusOK {
			t.Fatalf("reading %s was passed on", id)
		}
		if status, _ := h.request(http.MethodDelete, "/memory/items/"+url.PathEscape(id), ""); status == http.StatusNoContent {
			t.Fatalf("deleting %s was passed on", id)
		}
	}
	for _, id := range []string{page.Memories[0].ID, "anas-photos"} {
		if status, body := h.request(http.MethodDelete, "/memory/items/"+url.PathEscape(id), ""); status != http.StatusNoContent {
			t.Fatalf("delete %s = %d %s", id, status, body)
		}
	}
	// Clearing everything, conversations too: the one with a turn writing to it stays.
	status, body = h.request(http.MethodPost, "/memory/clear", `{"conversations":true}`)
	if status != http.StatusOK || body != `{"busy":1,"conversations":2,"memories":1,"skills":1}` {
		t.Fatalf("clear = %d %s", status, body)
	}
	if _, body := h.request(http.MethodGet, "/memory", ""); body != `{"memories":[],"skills":[]}` {
		t.Fatalf("after clearing = %s", body)
	}
	for _, call := range h.serve.rest {
		if strings.Contains(call, "profile") || strings.HasPrefix(call, "PUT") {
			t.Fatalf("%s reached Hermes", call)
		}
	}
}
