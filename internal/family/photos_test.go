package family

import (
	"context"
	"slices"
	"testing"
	"time"
)

// channelPhotos lists a baby's records as "date:photo,photo" lines, newest
// first, for comparing whole timelines.
func channelPhotos(t *testing.T, f *fixture, babyID string) []string {
	t.Helper()
	var lines []string
	for _, record := range must(f.store.Records(context.Background(), mom, babyID, "", "")).Records {
		line := record.OccurredOn + ":"
		for i, photo := range record.Photos {
			if i > 0 {
				line += ","
			}
			line += photo.AssetID
		}
		lines = append(lines, line)
	}
	return lines
}

func TestAPhotoRecordBecomesOneRecordPerDay(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	f.photos.add("morning", at("2026-09-20 10:00"), true)
	f.photos.add("evening", at("2026-09-20 18:00"), true)
	f.photos.add("train", at("2026-09-22 09:00"), true)
	// Without a capture time a photo dates from its upload; a camera clock
	// running ahead files it under today.
	f.photos.photos["saved"] = Photo{ID: "saved", ImportedAt: time.Date(2026, 9, 25, 21, 0, 0, 0, time.Local), Shared: true}
	f.photos.add("ahead", at("2027-01-01 08:00"), true)

	input := RecordInput{RequestKey: "trip", Kind: KindPhotos, Text: "回老家",
		Photos: []string{"morning", "train", "evening", "saved", "ahead"}}
	latest := must(f.store.AddRecord(ctx, grandma, baby.ID, input))
	if latest.OccurredOn != "2026-10-29" || len(latest.Photos) != 1 || latest.Photos[0].AssetID != "ahead" || latest.Text != "回老家" {
		t.Fatalf("the latest record = %+v", latest)
	}
	want := []string{"2026-10-29:ahead", "2026-09-25:saved", "2026-09-22:train", "2026-09-20:morning,evening"}
	if got := channelPhotos(t, f, baby.ID); !slices.Equal(got, want) {
		t.Fatalf("timeline = %v\nwant       %v", got, want)
	}
	for _, record := range must(f.store.Records(ctx, mom, baby.ID, "", "")).Records {
		if record.Text != "回老家" || record.AuthorID != grandma.AccountID {
			t.Fatalf("a day's record = %+v; want grandma's text on each", record)
		}
	}
	// A retry returns the latest record and adds nothing.
	if again := must(f.store.AddRecord(ctx, grandma, baby.ID, input)); again.ID != latest.ID {
		t.Fatalf("a retried request returned %s, want %s", again.ID, latest.ID)
	}
	if got := channelPhotos(t, f, baby.ID); len(got) != 4 {
		t.Fatalf("after a retry the timeline = %v", got)
	}
}

func TestPhotosAlreadyInTheChannelAreLeftOut(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	second := must(f.store.AddBaby(ctx, mom, BabyFields{Name: "二宝", Sex: "male", BirthDate: "2026-10-01"}))
	for _, id := range []string{"a", "b", "c", "d"} {
		f.photos.add(id, at("2026-10-20 09:00"), true)
	}
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"a", "b"}}))
	must(f.store.AddRecord(ctx, mom, second.ID, RecordInput{Kind: KindPhotos, Photos: []string{"c"}}))

	if got := must(f.store.AlbumContains(ctx, dad, baby.ID, []string{"c", "b", "x", "a", "b"})); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("album contains = %v, want b and a in the order asked", got)
	}
	_, err := f.store.AlbumContains(ctx, tenant, baby.ID, []string{"a"})
	wantErr(t, err, ErrNotMember)
	_, err = f.store.AlbumContains(ctx, dad, baby.ID, make([]string, MaxAlbumContains+1))
	wantErr(t, err, ErrInvalid)

	// Each channel leaves out what it has: the first gets c and d, the
	// second a and d.
	f.now = f.now.Add(time.Minute)
	record := must(f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindPhotos,
		Photos: []string{"a", "c", "d"}, AlsoBabies: []string{second.ID}}))
	if len(record.Photos) != 2 || record.Photos[0].AssetID != "c" || record.Photos[1].AssetID != "d" {
		t.Fatalf("record = %+v; want c and d", record.Photos)
	}
	if got := channelPhotos(t, f, second.ID); !slices.Equal(got, []string{"2026-10-20:a,d", "2026-10-20:c"}) {
		t.Fatalf("second baby's timeline = %v", got)
	}
	_, err = f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"a", "d"}})
	wantErr(t, err, ErrInChannel)

	// A journal or milestone may show a photo the album has.
	must(f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindJournal, OccurredOn: "2026-10-20", Text: "看海", Photos: []string{"a"}}))
	if album := must(f.store.Album(ctx, dad, baby.ID, "")); len(album.Photos) != 4 {
		t.Fatalf("album = %+v; want each photo once", album.Photos)
	}

	// A deleted record lets its photos in again.
	wantErr(t, f.store.DeleteRecord(ctx, dad, record.ID), nil)
	if got := must(f.store.AlbumContains(ctx, dad, baby.ID, []string{"c", "d"})); len(got) != 0 {
		t.Fatalf("after deleting the record, album contains %v", got)
	}
	must(f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"c"}}))
}
