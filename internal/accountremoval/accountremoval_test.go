package accountremoval_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/accountremoval"
)

// The Product Service only names the account; the service acts on what the
// Host Agent says and answers what it handed over.
func TestARemovalActsOnTheHostAgentsWordAndNamesWhatItHandedOver(t *testing.T) {
	deletions := map[string]accountid.AccountDeletion{
		"user:alice": {UserID: "user:alice", Username: "alice", Library: "transfer", AdminID: "user:owner"},
	}
	var removed []accountid.AccountDeletion
	lookup := func(_ context.Context, userID string) (accountid.AccountDeletion, error) {
		deletion, ok := deletions[userID]
		if !ok {
			return accountid.AccountDeletion{}, accountid.ErrNotDeleting
		}
		return deletion, nil
	}
	remove := func(_ context.Context, deletion accountid.AccountDeletion) (accountremoval.Result, error) {
		removed = append(removed, deletion)
		return accountremoval.Result{Album: "alice 的照片 (2)"}, nil
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	socket := filepath.Join(t.TempDir(), "service.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: accountremoval.Route(lookup, remove, nil, next)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	ctx := context.Background()
	result, err := accountremoval.Request(ctx, socket, "user:alice")
	if err != nil || result.Album != "alice 的照片 (2)" {
		t.Fatalf("Request(alice) = %+v, %v", result, err)
	}
	if len(removed) != 1 || removed[0].AdminID != "user:owner" {
		t.Fatalf("removed %+v", removed)
	}
	// An account whose deletion has not started is refused, whatever asks.
	if _, err := accountremoval.Request(ctx, socket, "user:bob"); err == nil {
		t.Fatal("an account that is not being deleted was removed")
	}
	if len(removed) != 1 {
		t.Fatalf("removed %+v", removed)
	}
	// Other paths go to the service's own API.
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", socket)
	}}}
	response, err := client.Get("http://service/api/v1/photos")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusTeapot {
		t.Fatalf("the API answered %d", response.StatusCode)
	}
	if _, err := accountremoval.Request(ctx, filepath.Join(t.TempDir(), "absent.sock"), "user:alice"); err == nil || errors.Is(err, accountid.ErrNotDeleting) {
		t.Fatalf("Request to no service = %v", err)
	}
}
