package photos_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// Months follow the device's time zone, the one capture times without an
// offset are read in, so a timeline's month sections match its day sections.
func TestTimelineCountsAndListsMonthsInTheDeviceTimeZone(t *testing.T) {
	ctx := context.Background()
	shanghai := time.FixedZone("CST", 8*3600)
	service, err := photos.Open(filepath.Join(t.TempDir(), "photos"), photos.Options{
		Now: newClock().Now, DisableCapacityReserve: true, Location: shanghai,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	private, _ := libraries(t, service, alice)
	photo := func(name, taken string) photos.Asset {
		return importPhoto(t, service, alice, private.ID, "", name, exifJPEG(t, 8, 8, 1, taken, ""))
	}
	// Early on 1 May in Shanghai is still 30 April in UTC.
	photo("may.jpg", "2026:05:01 07:00:00")
	photo("april.jpg", "2026:04:30 23:30:00")
	photo("march.jpg", "2026:03:15 12:00:00")
	photo("march-2.jpg", "2026:03:20 12:00:00")
	trashed := photo("trashed.jpg", "2026:03:16 12:00:00")
	if _, err := service.Trash(ctx, alice, trashed.ID); err != nil {
		t.Fatalf("Trash() error = %v", err)
	}
	// Without a capture time a photo counts in the month it was imported.
	importPhoto(t, service, alice, private.ID, "", "plain.png", encodePNG(30))

	months, err := service.TimelineMonths(ctx, alice, private.ID)
	want := []photos.TimelineMonth{{Month: "2026-10", Photos: 1}, {Month: "2026-05", Photos: 1}, {Month: "2026-04", Photos: 1}, {Month: "2026-03", Photos: 2}}
	if err != nil || !slices.Equal(months, want) {
		t.Fatalf("TimelineMonths() = %v, %v; want %v", months, err, want)
	}
	inMonth := func(month, cursor string, limit int) photos.Page {
		t.Helper()
		page, err := service.TimelineInMonth(ctx, alice, private.ID, month, cursor, limit)
		if err != nil {
			t.Fatalf("TimelineInMonth(%s) error = %v", month, err)
		}
		return page
	}
	for month, wantNames := range map[string][]string{"2026-05": {"may.jpg"}, "2026-04": {"april.jpg"}, "2026-01": {}} {
		if got := names(inMonth(month, "", 0).Assets); !slices.Equal(got, wantNames) {
			t.Fatalf("TimelineInMonth(%s) = %v, want %v", month, got, wantNames)
		}
	}
	first := inMonth("2026-03", "", 1)
	second := inMonth("2026-03", first.Next, 1)
	if got := append(names(first.Assets), names(second.Assets)...); !slices.Equal(got, []string{"march-2.jpg", "march.jpg"}) || second.Next != "" {
		t.Fatalf("March in pages of one = %v, last next %q", got, second.Next)
	}

	for _, month := range []string{"2026-13", "2026-5", "May", "2026-03-01"} {
		if _, err := service.TimelineInMonth(ctx, alice, private.ID, month, "", 0); !errors.Is(err, photos.ErrInvalidMonth) {
			t.Fatalf("TimelineInMonth(%q) error = %v, want ErrInvalidMonth", month, err)
		}
	}
	if _, err := service.TimelineMonths(ctx, bob, private.ID); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("another member's months error = %v, want ErrNotFound", err)
	}
	if _, err := service.TimelineInMonth(ctx, bob, private.ID, "2026-05", "", 0); !errors.Is(err, photos.ErrNotFound) {
		t.Fatalf("another member's month error = %v, want ErrNotFound", err)
	}
}
