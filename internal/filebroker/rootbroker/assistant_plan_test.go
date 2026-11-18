package rootbroker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accountid"
	"github.com/zhongwater123/A-NAS/internal/filebroker/wire"
)

// A planning grant (docs/architecture/montage-center.md, 规划授权) is the
// member's own, left behind their live session, for the waker alone.
func TestAPlanningGrantIsTheSignedInMembersAndRootsAlone(t *testing.T) {
	plans := filepath.Join(t.TempDir(), "assistant-plans")
	h := newAssistantHarness(t, func(config *Config) { config.AssistantPlans = plans })
	member := accountid.WithSessionToken(context.Background(), "member-token")
	plan, err := h.client.RegisterAssistantPlan(member)
	if err != nil || plan != wire.PlanRegistered {
		t.Fatalf("RegisterAssistantPlan() = %q, %v", plan, err)
	}
	raw, err := os.ReadFile(filepath.Join(plans, "alice"))
	if err != nil {
		t.Fatal(err)
	}
	var grant planGrant
	if err := json.Unmarshal(raw, &grant); err != nil || grant.Rounds != 5 {
		t.Fatalf("grant = %s, %v", raw, err)
	}
	if issued, err := time.Parse(time.RFC3339, grant.IssuedAt); err != nil || time.Since(issued) > time.Minute {
		t.Fatalf("issued at %q, %v", grant.IssuedAt, err)
	}
	for _, path := range []string{plans, filepath.Join(plans, "alice")} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s: %v %v, want root's alone", path, info, err)
		}
	}
	// The administrator has no assistant here: no grant, and saying so is no failure.
	admin := accountid.WithSessionToken(context.Background(), "admin-token")
	if plan, err := h.client.RegisterAssistantPlan(admin); err != nil || plan != wire.PlanNoAssistant {
		t.Fatalf("for an account without VNAS = %q, %v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(plans, "owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a grant for an account without VNAS: %v", err)
	}
	stranger := accountid.WithSessionToken(context.Background(), "no-such-token")
	if _, err := h.client.RegisterAssistantPlan(stranger); err == nil {
		t.Fatal("a grant without a live session")
	}
}

func TestNoPlanningGrantWithoutTheModelKey(t *testing.T) {
	plans := filepath.Join(t.TempDir(), "assistant-plans")
	h := newAssistantHarness(t, func(config *Config) {
		config.AssistantPlans = plans
		config.AssistantOn = func() bool { return false }
	})
	member := accountid.WithSessionToken(context.Background(), "member-token")
	if plan, err := h.client.RegisterAssistantPlan(member); err != nil || plan != wire.PlanDeviceOff {
		t.Fatalf("without the key = %q, %v", plan, err)
	}
	if _, err := os.Stat(filepath.Join(plans, "alice")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a grant without the key: %v", err)
	}
}
