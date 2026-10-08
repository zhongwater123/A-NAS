package main

import (
	"context"
	"testing"
)

type recordingVolumeGuard struct {
	root  string
	write bool
}

func (g *recordingVolumeGuard) Check(_ context.Context, root string, write bool) error {
	g.root = root
	g.write = write
	return nil
}

// File writes and app folders both go through the Host Agent; the Product
// Service's sandbox shows the volume read-only (issue #38 follow-up).
func TestVolumeChecksNeverRequireProductServiceWriteAccess(t *testing.T) {
	recorder := &recordingVolumeGuard{}
	guard := sandboxedVolumeGuard{recorder}
	if err := guard.Check(context.Background(), "/srv/a-nas/data", true); err != nil {
		t.Fatal(err)
	}
	if recorder.root != "/srv/a-nas/data" {
		t.Fatalf("guard root = %q", recorder.root)
	}
	if recorder.write {
		t.Fatal("a write check required direct Product Service write access")
	}
}
