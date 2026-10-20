package personalization_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/personalization"
)

func TestTheScreenSleepsAfterThirtyMinutesUntilChanged(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "personalization.db")
	store, err := personalization.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	display, err := store.LocalDisplay(ctx)
	if err != nil || display.SleepAfterMinutes != 30 {
		t.Fatalf("default = %#v, %v", display, err)
	}
	if _, err := store.SetLocalDisplay(ctx, personalization.LocalDisplay{SleepAfterMinutes: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := personalization.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if display, err := reopened.LocalDisplay(ctx); err != nil || display.SleepAfterMinutes != 10 {
		t.Fatalf("after reopening = %#v, %v", display, err)
	}
	if _, err := reopened.SetLocalDisplay(ctx, personalization.LocalDisplay{SleepAfterMinutes: 0}); err != nil {
		t.Fatalf("never: %v", err)
	}
	if display, _ := reopened.LocalDisplay(ctx); display.SleepAfterMinutes != 0 {
		t.Fatalf("never was stored as %#v", display)
	}
}

func TestOnlyTheOfferedDurationsAreAccepted(t *testing.T) {
	store, err := personalization.Open(filepath.Join(t.TempDir(), "personalization.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, minutes := range []int{-1, 1, 15, 45, 120} {
		if _, err := store.SetLocalDisplay(context.Background(), personalization.LocalDisplay{SleepAfterMinutes: minutes}); !errors.Is(err, personalization.ErrInvalidSetting) {
			t.Errorf("%d minutes: err = %v", minutes, err)
		}
	}
	if display, _ := store.LocalDisplay(context.Background()); display.SleepAfterMinutes != 30 {
		t.Fatalf("a refused value changed the setting to %#v", display)
	}
}
