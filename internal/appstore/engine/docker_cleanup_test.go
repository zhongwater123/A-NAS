package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/client"
)

func TestCleanupRejectsMutableImageRecordsBeforeContactingDocker(t *testing.T) {
	for _, reference := range []string{"example/app:latest", "example/app:v1", "sha256:invalid"} {
		t.Run(reference, func(t *testing.T) {
			// A nil client makes any accidental lookup of the tag fail the test.
			_, err := (DockerInspector{}).CleanupImages(context.Background(), "a-nas-test", []string{reference})
			if err == nil {
				t.Fatal("mutable or invalid reference accepted for deletion")
			}
		})
	}
}

func TestImageCleanupDoesNotFollowTagsAfterIDConflict(t *testing.T) {
	id := "sha256:" + strings.Repeat("a", 64)
	var deletions atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			if all := r.URL.Query().Get("all"); all != "1" && all != "true" {
				t.Errorf("must include stopped containers: %s", r.URL)
			}
			_, _ = fmt.Fprintln(w, "[]")
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/images/"+id):
			if r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true" {
				t.Error("forced image deletion")
			}
			deletions.Add(1)
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprintln(w, `{"message":"image has multiple tags"}`)
		default:
			t.Errorf("cleanup must not follow a mutable tag after ID conflict: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	err = (DockerInspector{Client: cli}).RemoveUnusedImages(context.Background(), []string{id})
	if err == nil || deletions.Load() != 1 {
		t.Fatalf("delete count = %d, error = %v", deletions.Load(), err)
	}
}
