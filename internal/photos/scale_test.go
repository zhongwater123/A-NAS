package photos

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// TestScaleBaseline seeds the first capacity baseline of the photo library
// specification, 4 members and 20,000 photos, through the real import path,
// and times the reads the desktop makes. It takes minutes, so it runs only
// with ANAS_PHOTO_SCALE=1; the budget catches query plans that grow with the
// library, while device numbers come from the Experimental NAS.
func TestScaleBaseline(t *testing.T) {
	if os.Getenv("ANAS_PHOTO_SCALE") == "" {
		t.Skip("set ANAS_PHOTO_SCALE=1 to run the 4-member, 20,000-photo baseline")
	}
	const (
		perLibrary = 4000 // four private libraries and the shared one
		budget     = 100 * time.Millisecond
	)
	ctx := context.Background()
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Add(int64(time.Minute))).UTC() }
	service, err := Open(filepath.Join(t.TempDir(), "photos"), Options{Now: now, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	members := []Principal{
		{UserID: "user:alice", Username: "alice"}, {UserID: "user:bob", Username: "bob"},
		{UserID: "user:carol", Username: "carol"}, {UserID: "user:dave", Username: "dave", Admin: true},
	}
	private := make(map[string]Library)
	var shared Library
	for _, member := range members {
		libraries, err := service.Libraries(ctx, member)
		if err != nil {
			t.Fatal(err)
		}
		for _, lib := range libraries {
			if lib.Kind == LibraryKindShared {
				shared = lib
			} else {
				private[member.UserID] = lib
			}
		}
	}

	// Most uploads land in the library root, as the desktop uploads there; a
	// fifth go to one of 20 directories. One photo in 20 is a copy of another
	// member's, so duplicate hints have work to do, and one in 100 is trashed.
	seeded := time.Now()
	var sample []Asset
	directories := make(map[string][]Directory)
	seed := func(uploader Principal, lib Library, offset int) {
		for d := range 20 {
			directory, err := service.CreateDirectory(ctx, uploader, lib.ID, "", fmt.Sprintf("album-%02d", d))
			if err != nil {
				t.Fatal(err)
			}
			directories[lib.ID] = append(directories[lib.ID], directory)
		}
		for i := range perLibrary {
			content := offset + i
			if i%20 == 0 && offset > 0 {
				content = i // the same original as the first library's
			}
			directoryID := ""
			if i%5 == 0 {
				directoryID = directories[lib.ID][i%20].ID
			}
			asset, err := service.Import(ctx, uploader, ImportRequest{
				LibraryID: lib.ID, DirectoryID: directoryID, Name: fmt.Sprintf("IMG_%05d.png", i), Content: bytes.NewReader(syntheticPNG(t, content)),
			})
			if err != nil {
				t.Fatalf("import %d into %s: %v", i, lib.ID, err)
			}
			if i%100 == 99 {
				if _, err := service.Trash(ctx, uploader, asset.ID); err != nil {
					t.Fatal(err)
				}
			}
			if i == perLibrary/2 {
				sample = append(sample, asset)
			}
		}
	}
	for n, member := range members {
		seed(member, private[member.UserID], n*perLibrary)
	}
	seed(members[0], shared, len(members)*perLibrary)
	imported := time.Since(seeded)
	for {
		processed, err := service.ProcessMediaJob(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !processed {
			break
		}
	}
	t.Logf("seeded %d photos in %s (%.1f ms per import), thumbnails in %s",
		5*perLibrary, imported.Round(time.Second), float64(imported.Milliseconds())/float64(5*perLibrary), (time.Since(seeded) - imported).Round(time.Second))

	alice, bob, dave := members[0], members[1], members[3]
	viewer := dave
	viewer.Viewing = []ViewingGrant{{GrantID: "viewing:1", OwnerUserID: alice.UserID, ExpiresAt: time.Unix(0, clock.Load()).Add(24 * time.Hour)}}
	aliceLibrary := private[alice.UserID]
	deepCursor := func(listing func(cursor string) (string, error), pages int) string {
		cursor := ""
		for range pages {
			next, err := listing(cursor)
			if err != nil {
				t.Fatal(err)
			}
			cursor = next
		}
		return cursor
	}
	timelineCursor := deepCursor(func(cursor string) (string, error) {
		page, err := service.Timeline(ctx, alice, aliceLibrary.ID, cursor, 120)
		return page.Next, err
	}, 20)
	rootCursor := deepCursor(func(cursor string) (string, error) {
		listing, err := service.ListDirectory(ctx, alice, aliceLibrary.ID, "", cursor, 100)
		return listing.Next, err
	}, 20)

	operations := []struct {
		name string
		run  func() error
	}{
		{"libraries (member)", func() error { _, err := service.Libraries(ctx, alice); return err }},
		{"libraries (administrator viewing)", func() error { _, err := service.Libraries(ctx, viewer); return err }},
		{"timeline first page (private)", func() error { _, err := service.Timeline(ctx, alice, aliceLibrary.ID, "", 120); return err }},
		{"timeline page 21 (private)", func() error { _, err := service.Timeline(ctx, alice, aliceLibrary.ID, timelineCursor, 120); return err }},
		{"timeline first page (shared)", func() error { _, err := service.Timeline(ctx, bob, shared.ID, "", 120); return err }},
		{"timeline first page (viewing)", func() error { _, err := service.Timeline(ctx, viewer, aliceLibrary.ID, "", 120); return err }},
		{"root listing first page", func() error { _, err := service.ListDirectory(ctx, alice, aliceLibrary.ID, "", "", 100); return err }},
		{"root listing page 21", func() error {
			_, err := service.ListDirectory(ctx, alice, aliceLibrary.ID, "", rootCursor, 100)
			return err
		}},
		{"directory listing", func() error {
			_, err := service.ListDirectory(ctx, alice, aliceLibrary.ID, directories[aliceLibrary.ID][3].ID, "", 100)
			return err
		}},
		{"get photo", func() error { _, err := service.Get(ctx, alice, sample[0].ID); return err }},
		{"trash", func() error { _, err := service.ListTrash(ctx, alice, aliceLibrary.ID); return err }},
	}
	var slow []string
	for _, operation := range operations {
		median, worst := measure(t, 30, operation.run)
		t.Logf("%-36s median %9s  max %9s", operation.name, median.Round(10*time.Microsecond), worst.Round(10*time.Microsecond))
		if median > budget {
			slow = append(slow, operation.name)
		}
	}
	if len(slow) > 0 {
		t.Errorf("over the %s budget at 20,000 photos: %v", budget, slow)
	}
}

func measure(t *testing.T, runs int, operation func() error) (median, worst time.Duration) {
	t.Helper()
	durations := make([]time.Duration, 0, runs)
	for range runs {
		start := time.Now()
		if err := operation(); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(start))
	}
	slices.Sort(durations)
	return durations[len(durations)/2], durations[len(durations)-1]
}

// syntheticPNG returns a small PNG whose bytes are unique to n.
func syntheticPNG(t *testing.T, n int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	binary.BigEndian.PutUint32(img.Pix, uint32(n))
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

// TestScaleThumbnailCost times one thumbnail of a 12-megapixel camera JPEG,
// which bounds how long the first thumbnail backlog of a large import takes.
func TestScaleThumbnailCost(t *testing.T) {
	if os.Getenv("ANAS_PHOTO_SCALE") == "" {
		t.Skip("set ANAS_PHOTO_SCALE=1 to time a 12-megapixel thumbnail")
	}
	img := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	random := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		// A gradient with sensor-like noise compresses like a photo.
		img.Pix[i] = uint8(i/4%4000/16) + uint8(random.IntN(24))
	}
	var original bytes.Buffer
	if err := jpeg.Encode(&original, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	median, worst := measure(t, 5, func() error {
		_, _, _, err := renderThumbnail(bytes.NewReader(original.Bytes()), 6)
		return err
	})
	t.Logf("12-megapixel JPEG (%.1f MiB): thumbnail median %s, max %s; 20,000 such photos take about %s",
		float64(original.Len())/(1<<20), median.Round(time.Millisecond), worst.Round(time.Millisecond), (median * 20000).Round(time.Minute))
}
