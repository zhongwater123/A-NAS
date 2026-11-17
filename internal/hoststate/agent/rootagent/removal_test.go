package rootagent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent/rootagent"
)

// removingHost records the removals it is asked to make. With block set, a
// removal waits for it and then says on finished whether its context ended.
type removingHost struct {
	agentCredentialProvisioner
	removed  []accountid.AccountRemoval
	block    chan struct{}
	finished chan error
}

func (h *removingHost) RemoveAccount(ctx context.Context, removal accountid.AccountRemoval) (string, error) {
	if h.block != nil {
		<-h.block
		h.finished <- ctx.Err()
		return "", nil
	}
	h.removed = append(h.removed, removal)
	if removal.TransferTo == "" {
		return "", nil
	}
	return removal.Folder + " (2)", nil
}

// deletionDirectory is the control plane as the Host Agent reads it.
type deletionDirectory map[string]accountid.AccountRemoval

func (d deletionDirectory) AccountRemoval(_ context.Context, userID string) (accountid.AccountRemoval, error) {
	removal, ok := d[userID]
	if !ok {
		return accountid.AccountRemoval{}, accountid.ErrNotDeleting
	}
	return removal, nil
}

// The Host Agent removes what the control plane says, whatever the request
// carries besides the account, and answers the folder the files went to.
func TestTheHostAgentRemovesOnlyWhatTheControlPlaneSays(t *testing.T) {
	alice := accountid.AccountRemoval{
		UserID: "user:alice", Username: "alice", UID: 20101, PrivateSpaceID: "space:alice",
		TransferTo: "owner", TransferToUID: 20100, Folder: "alice 的文件",
	}
	host := &removingHost{}
	client := agent.NewClient(serve(t, rootagent.NewOperationsHandler(rootagent.Services{
		Credentials: host, Deletions: deletionDirectory{alice.UserID: alice},
	}, discardLogger())))
	ctx := context.Background()
	// What the Product Service claims besides the account is not used.
	folder, err := client.RemoveAccount(ctx, accountid.AccountRemoval{UserID: "user:alice", Username: "bob", TransferTo: "mallory"})
	if err != nil || folder != "alice 的文件 (2)" {
		t.Fatalf("RemoveAccount() = %q, %v", folder, err)
	}
	if len(host.removed) != 1 || host.removed[0] != alice {
		t.Fatalf("removed %+v, want %+v", host.removed, alice)
	}
	// An account whose deletion has not started is not removed.
	if _, err := client.RemoveAccount(ctx, accountid.AccountRemoval{UserID: "user:bob", Username: "bob"}); !errors.Is(err, accountid.ErrNotDeleting) {
		t.Fatalf("RemoveAccount(bob) error = %v, want ErrNotDeleting", err)
	}
	if len(host.removed) != 1 {
		t.Fatalf("removed %+v", host.removed)
	}
	// Without the control plane, nothing is removed.
	alone := agent.NewClient(serve(t, rootagent.NewOperationsHandler(rootagent.Services{Credentials: host}, discardLogger())))
	if _, err := alone.RemoveAccount(ctx, accountid.AccountRemoval{UserID: "user:alice"}); err == nil {
		t.Fatal("a removal ran without the control plane")
	}
}

// A removal outlives a client that stops waiting for it, so a long copy is
// not cut off and started again on every attempt.
func TestARemovalOutlivesAClientThatStopsWaiting(t *testing.T) {
	alice := accountid.AccountRemoval{UserID: "user:alice", Username: "alice", UID: 20101, PrivateSpaceID: "space:alice"}
	host := &removingHost{block: make(chan struct{}), finished: make(chan error, 1)}
	client := agent.NewClient(serve(t, rootagent.NewOperationsHandler(rootagent.Services{
		Credentials: host, Deletions: deletionDirectory{alice.UserID: alice},
	}, discardLogger())))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := client.RemoveAccount(ctx, accountid.AccountRemoval{UserID: alice.UserID}); err == nil {
		t.Fatal("the client did not stop waiting")
	}
	close(host.block)
	select {
	case err := <-host.finished:
		if err != nil {
			t.Fatalf("the removal's context ended with the client: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the removal did not finish")
	}
}
