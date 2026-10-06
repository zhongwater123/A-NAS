package hoststate_test

import (
	"testing"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

func TestResourceIDAcceptsOpaqueStableIdentifier(t *testing.T) {
	id, err := hoststate.NewResourceID("disk:fake-data-01")
	if err != nil {
		t.Fatalf("NewResourceID() error = %v", err)
	}
	if got := id.String(); got != "disk:fake-data-01" {
		t.Fatalf("ResourceID.String() = %q, want %q", got, "disk:fake-data-01")
	}
}

func TestResourceIDRejectsPathsAndBlankValues(t *testing.T) {
	for _, value := range []string{"", "   ", "/dev/sda", "dev/disk/by-id/example", `C:\\disk`} {
		t.Run(value, func(t *testing.T) {
			if _, err := hoststate.NewResourceID(value); err == nil {
				t.Fatalf("NewResourceID(%q) succeeded, want error", value)
			}
		})
	}
}
