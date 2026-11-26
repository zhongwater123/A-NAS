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

// A planning grant (docs/architecture/videocut-center.md, 规划授权) is the
// member's own, left behind their live session, for the waker alone.
func TestAPlanningGrantIsTheSignedInMembersAndRootsAlone(t *testing.T) {
	plans := filepath.Join(t.TempDir(), "assistant", "plans")
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
	plans := filepath.Join(t.TempDir(), "assistant", "plans")
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

// The grants are written only into a directory that is the broker's own and
// nobody else's to change. One that others can write to, or that is a link
// someone left in its place, could hold grants no member registered, or take
// the broker's write somewhere else: nothing is written, and the request is
// told to try later.
func TestAPlanningGrantIsWrittenOnlyIntoTheBrokersOwnDirectory(t *testing.T) {
	member := accountid.WithSessionToken(context.Background(), "member-token")
	for name, prepare := range map[string]func(t *testing.T, plans, elsewhere string){
		"a directory others can write to": func(t *testing.T, plans, _ string) {
			if err := os.Mkdir(plans, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(plans, 0o777); err != nil {
				t.Fatal(err)
			}
		},
		"a link to another directory": func(t *testing.T, plans, elsewhere string) {
			if err := os.Symlink(elsewhere, plans); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			plans, elsewhere := filepath.Join(dir, "plans"), filepath.Join(dir, "elsewhere")
			if err := os.Mkdir(elsewhere, 0o700); err != nil {
				t.Fatal(err)
			}
			prepare(t, plans, elsewhere)
			h := newAssistantHarness(t, func(config *Config) { config.AssistantPlans = plans })
			if plan, err := h.client.RegisterAssistantPlan(member); err == nil {
				t.Fatalf("RegisterAssistantPlan() = %q, want a failure", plan)
			}
			for _, where := range []string{plans, elsewhere} {
				if entries, err := os.ReadDir(where); err != nil || len(entries) != 0 {
					t.Fatalf("%s holds %v, %v", where, entries, err)
				}
			}
		})
	}
	// A link left where the grant goes is replaced, not followed.
	dir := t.TempDir()
	plans, target := filepath.Join(dir, "plans"), filepath.Join(dir, "target")
	if err := os.Mkdir(plans, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(plans, "alice")); err != nil {
		t.Fatal(err)
	}
	h := newAssistantHarness(t, func(config *Config) { config.AssistantPlans = plans })
	if plan, err := h.client.RegisterAssistantPlan(member); err != nil || plan != wire.PlanRegistered {
		t.Fatalf("RegisterAssistantPlan() = %q, %v", plan, err)
	}
	if info, err := os.Lstat(filepath.Join(plans, "alice")); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the grant: %v, %v", info, err)
	}
	if kept, err := os.ReadFile(target); err != nil || string(kept) != "untouched" {
		t.Fatalf("the link's target = %q, %v", kept, err)
	}
}
