package vectorindextest

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/vectorindex"
)

// A port that another process took before the server could bind it is not
// mistaken for the server. In CI a test's HTTP server took a gRPC port, and
// the client received an HTTP/1.1 answer.
func TestStartSkipsAPortAnotherProcessTook(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(taken) }()
	t.Cleanup(func() { _ = server.Close() })
	port := taken.Addr().(*net.TCPAddr).Port
	picks := 0
	pickPort = func(t testing.TB) int {
		picks++
		if picks == 2 { // the first server's gRPC port
			return port
		}
		return freePort(t)
	}
	t.Cleanup(func() { pickPort = freePort })

	instance := Start(t)
	if picks < 4 {
		t.Fatalf("Start picked %d ports; want it to start again on others", picks)
	}
	client := vectorindex.New(instance.Socket, nil)
	defer func() { _ = client.Close() }()
	if err := client.CreateCollection(context.Background(), vectorindex.Collection{Name: "probe", Dimensions: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(instance.Storage, "collections", "probe")); err != nil {
		t.Fatalf("the collection is not in the server's own storage: %v", err)
	}
}
