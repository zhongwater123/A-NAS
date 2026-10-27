package vectorindex_test

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/zhongwater123/A-NAS/internal/vectorindex"
	"github.com/zhongwater123/A-NAS/internal/vectorindex/vectorindextest"
)

// The client module, the pinned server and ServerVersion move together:
// Qdrant supports a client within one minor release of its server, and a
// single-node server cannot skip a minor release (the instance rebuilds
// instead).
func TestClientAndServerAreTheSameRelease(t *testing.T) {
	root := vectorindextest.RepositoryRoot(t)
	manifest, err := vectorindextest.Manifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manifest.Version, vectorindex.ServerVersion+".") {
		t.Errorf("deploy/qdrant/qdrant.json pins %s, the client is built for %s", manifest.Version, vectorindex.ServerVersion)
	}
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, require := range module.Require {
		if require.Mod.Path == "github.com/qdrant/go-client" {
			found = true
			if !strings.HasPrefix(require.Mod.Version, "v"+vectorindex.ServerVersion+".") {
				t.Errorf("go.mod requires go-client %s, the server is %s", require.Mod.Version, vectorindex.ServerVersion)
			}
		}
	}
	if !found {
		t.Error("go.mod does not require github.com/qdrant/go-client")
	}
}

func TestPointIDIsAStableUUID(t *testing.T) {
	id := vectorindex.PointID("3f1a")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("PointID = %q, not a UUID", id)
	}
	if vectorindex.PointID("3f1a") != id || vectorindex.PointID("3f1b") == id {
		t.Fatal("PointID is not a function of the key")
	}
}

// Requests that would not mean what the caller meant are refused before any
// connection, and a search without a scope finds nothing.
func TestRequestsAreTyped(t *testing.T) {
	ctx := context.Background()
	client := vectorindex.New(filepath.Join(t.TempDir(), "missing.sock"), nil)
	defer func() { _ = client.Close() }()
	if hits, err := client.Search(ctx, "c", vectorindex.Query{Vector: []float32{1}, Scope: vectorindex.Scope{Field: "libraries"}, Limit: 10}); err != nil || hits != nil {
		t.Errorf("Search without scope values = %v, %v; want nothing", hits, err)
	}
	for name, err := range map[string]error{
		"unnamed scope": searchErr(client, vectorindex.Scope{Values: []string{"a"}}),
		"scope on key":  searchErr(client, vectorindex.Scope{Field: "key", Values: []string{"a"}}),
		"keyword key":   client.Upsert(ctx, "c", []vectorindex.Point{{Key: "a", Vector: []float32{1}, Keywords: map[string][]string{"key": {"b"}}}}),
		"key as field":  client.CreateCollection(ctx, vectorindex.Collection{Name: "c", Dimensions: 2, Keywords: []string{"key"}}),
		"no dimensions": client.CreateCollection(ctx, vectorindex.Collection{Name: "c"}),
		"no limit":      searchErrLimit(client, 0),
	} {
		if !errors.Is(err, vectorindex.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func searchErr(client *vectorindex.Client, scope vectorindex.Scope) error {
	_, err := client.Search(context.Background(), "c", vectorindex.Query{Vector: []float32{1}, Scope: scope, Limit: 10})
	return err
}

func searchErrLimit(client *vectorindex.Client, limit int) error {
	_, err := client.Search(context.Background(), "c", vectorindex.Query{Vector: []float32{1},
		Scope: vectorindex.Scope{Field: "libraries", Values: []string{"a"}}, Limit: limit})
	return err
}

func TestIndex(t *testing.T) {
	instance := vectorindextest.Start(t)
	ctx := context.Background()
	client := vectorindex.New(instance.Socket, nil)
	defer func() { _ = client.Close() }()

	if err := client.CreateCollection(ctx, vectorindex.Collection{Name: "images", Dimensions: 3, Keywords: []string{"libraries"}}); err != nil {
		t.Fatal(err)
	}
	points := []vectorindex.Point{
		{Key: "east", Vector: unit(1, 0, 0), Keywords: map[string][]string{"libraries": {"alice", "shared"}}},
		{Key: "north", Vector: unit(0, 1, 0), Keywords: map[string][]string{"libraries": {"bob"}}},
		{Key: "northeast", Vector: unit(1, 1, 0), Keywords: map[string][]string{"libraries": {"shared"}}},
	}
	if err := client.Upsert(ctx, "images", points); err != nil {
		t.Fatal(err)
	}

	hits, err := client.Search(ctx, "images", vectorindex.Query{Vector: unit(1, 0.2, 0),
		Scope: vectorindex.Scope{Field: "libraries", Values: []string{"alice", "shared"}}, Limit: 10, Exact: true})
	if err != nil {
		t.Fatal(err)
	}
	if keys := hitKeys(hits); !slices.Equal(keys, []string{"east", "northeast"}) {
		t.Errorf("search within alice and shared = %v, want east then northeast; bob's photo never", keys)
	}
	if hits[0].Score < hits[1].Score || hits[0].Score > 1.0001 {
		t.Errorf("scores = %v, want dot products, best first", hits)
	}

	// Replacing a point replaces its keywords.
	if err := client.Upsert(ctx, "images", []vectorindex.Point{{Key: "east", Vector: unit(1, 0, 0), Keywords: map[string][]string{"libraries": {"bob"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, "images", []string{"northeast", "never-stored"}); err != nil {
		t.Fatal(err)
	}
	hits, err = client.Search(ctx, "images", vectorindex.Query{Vector: unit(1, 0, 0),
		Scope: vectorindex.Scope{Field: "libraries", Values: []string{"alice", "shared"}}, Limit: 10})
	if err != nil || len(hits) != 0 {
		t.Errorf("after the move and the delete, alice and shared find %v, %v; want nothing", hits, err)
	}

	scanned := map[string][]string{}
	if err := client.Scan(ctx, "images", []string{"libraries"}, func(entry vectorindex.Entry) error {
		scanned[entry.Key] = entry.Keywords["libraries"]
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(scanned) != 2 || !slices.Equal(scanned["east"], []string{"bob"}) || !slices.Equal(scanned["north"], []string{"bob"}) {
		t.Errorf("scan = %v, want east and north, both bob's", scanned)
	}

	collections, err := client.Collections(ctx)
	if err != nil || !slices.Equal(collections, []string{"images"}) {
		t.Errorf("Collections = %v, %v", collections, err)
	}
	if err := client.DeleteCollection(ctx, "images"); err != nil {
		t.Fatal(err)
	}
	if collections, err := client.Collections(ctx); err != nil || len(collections) != 0 {
		t.Errorf("after delete, Collections = %v, %v", collections, err)
	}
}

// An idle client closes its connection, so that on the NAS the proxy exits
// and the instance stops; the next request connects again.
func TestIdleConnectionCloses(t *testing.T) {
	defer vectorindex.SetIdleTimeout(200 * time.Millisecond)()
	instance := vectorindextest.Start(t)
	ctx := context.Background()
	client := vectorindex.New(instance.Socket, nil)
	defer func() { _ = client.Close() }()

	if _, err := client.Collections(ctx); err != nil {
		t.Fatal(err)
	}
	if instance.Connections() == 0 {
		t.Fatal("no connection while in use")
	}
	deadline := time.Now().Add(10 * time.Second)
	for instance.Connections() > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still open after idling", instance.Connections())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := client.Collections(ctx); err != nil {
		t.Fatalf("after idling: %v", err)
	}
}

func hitKeys(hits []vectorindex.Hit) []string {
	keys := make([]string, len(hits))
	for i, hit := range hits {
		keys[i] = hit.Key
	}
	return keys
}

// unit is the normalised vector of its arguments.
func unit(values ...float32) []float32 {
	var norm float32
	for _, value := range values {
		norm += value * value
	}
	out := make([]float32, len(values))
	for i, value := range values {
		out[i] = value / float32(math.Sqrt(float64(norm)))
	}
	return out
}
