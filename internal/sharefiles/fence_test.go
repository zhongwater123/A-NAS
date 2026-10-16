package sharefiles

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/shares"
)

func TestFenceDrainsInFlightPublishAndRejectsLaterPublication(t *testing.T) {
	local := NewLocal(nil, nil)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = local.publish(context.Background(), "share", func() error { close(started); <-release; return nil })
	}()
	<-started
	fenced := make(chan struct{})
	go func() { _ = local.Fence(context.Background(), "share"); close(fenced) }()
	select {
	case <-fenced:
		t.Fatal("fence returned while a publish could still complete")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-finished
	<-fenced
	published := false
	err := local.publish(context.Background(), "share", func() error { published = true; return nil })
	if !errors.Is(err, shares.ErrInactive) || published {
		t.Fatalf("late publication: %v, %v", published, err)
	}
}
