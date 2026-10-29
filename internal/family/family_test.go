package family

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePhotos is the photo service as a test arranges it: which photos exist,
// which are shared, and which each account can see.
type fakePhotos struct {
	mu     sync.Mutex
	photos map[string]Photo
	hidden map[string]bool
	down   bool
}

func (f *fakePhotos) Lookup(_ context.Context, _ Actor, ids []string) (map[string]Photo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("photo service down")
	}
	found := map[string]Photo{}
	for _, id := range ids {
		if photo, ok := f.photos[id]; ok && !f.hidden[id] {
			found[id] = photo
		}
	}
	return found, nil
}

func (f *fakePhotos) add(id string, taken *time.Time, shared bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.photos[id] = Photo{ID: id, TakenAt: taken, Shared: shared}
}

func (f *fakePhotos) trash(id string, trashed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hidden[id] = trashed
}

type fixture struct {
	store    *Store
	photos   *fakePhotos
	now      time.Time
	accounts map[string]Account
	dir      string
}

var (
	mom      = Actor{AccountID: "u-mom", Username: "mom"}
	dad      = Actor{AccountID: "u-dad", Username: "dad"}
	grandma  = Actor{AccountID: "u-grandma", Username: "grandma"}
	tenant   = Actor{AccountID: "u-tenant", Username: "tenant"}
	accounts = []Actor{mom, dad, grandma, tenant}
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		photos:   &fakePhotos{photos: map[string]Photo{}, hidden: map[string]bool{}},
		now:      time.Date(2026, 10, 29, 10, 0, 0, 0, time.Local),
		accounts: map[string]Account{},
		dir:      t.TempDir(),
	}
	for _, actor := range accounts {
		f.accounts[actor.AccountID] = Account{ID: actor.AccountID, Username: actor.Username}
	}
	store, err := Open(f.dir, Options{
		Now:      func() time.Time { return f.now },
		Accounts: func(context.Context) (map[string]Account, error) { return f.accounts, nil },
		Photos:   f.photos,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	f.store = store
	return f
}

// family founds a family with mom as guardian, dad and grandma as members
// and one baby born on 2026-03-15.
func (f *fixture) family(t *testing.T) Baby {
	t.Helper()
	ctx := context.Background()
	must(f.store.Join(ctx, mom, "妈妈"))
	must(f.store.Join(ctx, dad, "爸爸"))
	must(f.store.Join(ctx, grandma, "外婆"))
	must(f.store.Approve(ctx, mom, dad.AccountID))
	must(f.store.Approve(ctx, mom, grandma.AccountID))
	return must(f.store.AddBaby(ctx, mom, BabyFields{Name: "小米粒", Sex: "female", BirthDate: "2026-03-15"}))
}

// must returns value, and fails the test by panicking with err, which names
// the line in its trace: Go lets a call with two results be the only
// argument, so there is no room for the test.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func at(value string) *time.Time {
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, time.Local)
	if err != nil {
		panic(err)
	}
	return &parsed
}

func TestFirstAccountFoundsTheFamilyAndGuardiansApproveTheRest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	overview := must(f.store.Overview(ctx, mom))
	if overview.Exists || overview.Me.Status != StatusNone {
		t.Fatalf("before anyone joined: %+v", overview)
	}
	if me := must(f.store.Join(ctx, mom, "妈妈")); me.Status != StatusMember || !me.Guardian {
		t.Fatalf("founder = %+v", me)
	}
	if me := must(f.store.Join(ctx, grandma, "外婆")); me.Status != StatusPending {
		t.Fatalf("second account = %+v", me)
	}
	// Waiting is not belonging: grandma sees nothing yet.
	waiting := must(f.store.Overview(ctx, grandma))
	if waiting.Me.Status != StatusPending || len(waiting.Members) != 0 || len(waiting.Babies) != 0 {
		t.Fatalf("pending overview = %+v", waiting)
	}
	_, err := f.store.Records(ctx, grandma, "baby_x", "", "")
	wantErr(t, err, ErrNotMember)
	// Only guardians approve.
	must(f.store.Join(ctx, dad, "爸爸"))
	_, err = f.store.Approve(ctx, grandma, dad.AccountID)
	wantErr(t, err, ErrNotMember)
	if member := must(f.store.Approve(ctx, mom, dad.AccountID)); !member.Guardian {
		t.Fatal("a father who joins is a guardian")
	}
	if member := must(f.store.Approve(ctx, mom, grandma.AccountID)); member.Guardian {
		t.Fatal("a grandmother who joins is a relative")
	}
	_, err = f.store.Approve(ctx, grandma, tenant.AccountID)
	wantErr(t, err, ErrForbidden)
	// A rejected account may ask again.
	must(f.store.Join(ctx, tenant, "舅舅"))
	if err := f.store.Reject(ctx, dad, tenant.AccountID); err != nil {
		t.Fatal(err)
	}
	if me := must(f.store.Overview(ctx, tenant)); me.Me.Status != StatusNone {
		t.Fatalf("rejected = %+v", me.Me)
	}
	_, err = f.store.Join(ctx, mom, "妈妈")
	wantErr(t, err, ErrConflict)
	_, err = f.store.Join(ctx, tenant, strings.Repeat("长", 9))
	wantErr(t, err, ErrInvalid)
}

func TestTheFamilyAlwaysKeepsAGuardian(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.family(t)
	must(f.store.SetGuardian(ctx, mom, dad.AccountID, false))
	_, err := f.store.SetGuardian(ctx, mom, mom.AccountID, false)
	wantErr(t, err, ErrLastGuardian)
	wantErr(t, f.store.Leave(ctx, mom), ErrLastGuardian)
	wantErr(t, f.store.Remove(ctx, grandma, dad.AccountID), ErrForbidden)
	// When the only guardian's account goes, the earliest member left takes
	// over, so the family can still approve people.
	delete(f.accounts, mom.AccountID)
	overview := must(f.store.Overview(ctx, dad))
	if !overview.Me.Guardian {
		t.Fatalf("dad after mom's account went: %+v", overview.Me)
	}
	if len(overview.Members) != 2 {
		t.Fatalf("members = %+v", overview.Members)
	}
	// Removing a member keeps what they added.
	baby := overview.Babies[0]
	record := must(f.store.AddRecord(ctx, grandma, baby.ID, RecordInput{Kind: KindJournal, Text: "今天会笑了", OccurredOn: "2026-10-28"}))
	if err := f.store.Remove(ctx, dad, grandma.AccountID); err != nil {
		t.Fatal(err)
	}
	if got := must(f.store.Record(ctx, dad, record.ID)); got.AuthorName != "grandma" {
		t.Fatalf("record after its author left = %+v", got)
	}
	_, err = f.store.Records(ctx, grandma, baby.ID, "", "")
	wantErr(t, err, ErrNotMember)
}

func TestGuardiansLookAfterBabies(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	if baby.Age.TotalMonths != 7 || baby.Age.Days != 14 {
		t.Fatalf("age on 2026-10-29 = %+v", baby.Age)
	}
	_, err := f.store.AddBaby(ctx, grandma, BabyFields{Name: "二宝", Sex: "male", BirthDate: "2026-10-01"})
	wantErr(t, err, ErrForbidden)
	_, err = f.store.AddBaby(ctx, mom, BabyFields{Name: "二宝", Sex: "male", BirthDate: "2026-11-01"})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.AddBaby(ctx, mom, BabyFields{Name: "二宝", Sex: "boy", BirthDate: "2026-10-01"})
	wantErr(t, err, ErrInvalid)
	second := must(f.store.AddBaby(ctx, dad, BabyFields{Name: "二宝", Sex: "male", BirthDate: "2026-10-01"}))
	// Two guardians editing at once: the second save is refused.
	edit := BabyFields{Name: "小米粒", Nickname: "米米", Sex: "female", BirthDate: "2026-03-15", Version: baby.Version}
	must(f.store.UpdateBaby(ctx, mom, baby.ID, edit))
	_, err = f.store.UpdateBaby(ctx, dad, baby.ID, edit)
	wantErr(t, err, ErrChanged)
	// Deleting asks for the name, and the trash keeps the baby 15 days.
	wantErr(t, f.store.DeleteBaby(ctx, mom, second.ID, "三宝"), ErrInvalid)
	if err := f.store.DeleteBaby(ctx, mom, second.ID, "二宝"); err != nil {
		t.Fatal(err)
	}
	overview := must(f.store.Overview(ctx, mom))
	if len(overview.Babies) != 1 || len(overview.DeletedBabies) != 1 {
		t.Fatalf("after deleting: %+v / %+v", overview.Babies, overview.DeletedBabies)
	}
	if relative := must(f.store.Overview(ctx, grandma)); len(relative.DeletedBabies) != 0 {
		t.Fatal("only guardians see babies they can restore")
	}
	must(f.store.RestoreBaby(ctx, mom, second.ID))
	if err := f.store.DeleteBaby(ctx, mom, second.ID, "二宝"); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.AddDate(0, 0, TrashDays+1)
	_, err = f.store.RestoreBaby(ctx, mom, second.ID)
	wantErr(t, err, ErrNotFound)
	if err := f.store.Purge(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPhotoRecordsShowOnlyWhatTheReaderCanSee(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	f.photos.add("p-park-1", at("2026-09-20 10:00"), true)
	f.photos.add("p-park-2", at("2026-09-21 10:00"), true)
	f.photos.add("p-old", nil, true)
	f.photos.add("p-private", at("2026-09-20 10:00"), false)
	_, err := f.store.AddRecord(ctx, grandma, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"p-private"}})
	wantErr(t, err, ErrInvalid)
	_, err = f.store.AddRecord(ctx, grandma, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"p-missing"}})
	wantErr(t, err, ErrInvalid)
	record := must(f.store.AddRecord(ctx, grandma, baby.ID, RecordInput{
		RequestKey: "k1", Kind: KindPhotos, Text: "公园散步", Photos: []string{"p-park-2", "p-park-1"},
	}))
	if record.OccurredOn != "2026-09-20" || record.Age == nil || record.Age.TotalMonths != 6 || len(record.Photos) != 2 {
		t.Fatalf("photo record = %+v", record)
	}
	// A retried request returns the same record.
	again := must(f.store.AddRecord(ctx, grandma, baby.ID, RecordInput{
		RequestKey: "k1", Kind: KindPhotos, Photos: []string{"p-park-2", "p-park-1"},
	}))
	if again.ID != record.ID {
		t.Fatal("a retried request added a second record")
	}
	undated := must(f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"p-old"}}))
	if undated.OccurredOn != "2026-10-29" {
		t.Fatalf("a photo without a capture time dates from today, got %s", undated.OccurredOn)
	}
	// In the trash of the photo library, a photo leaves the channel; back,
	// it returns. A record whose photos are all gone disappears.
	f.photos.trash("p-old", true)
	page := must(f.store.Records(ctx, mom, baby.ID, "", ""))
	if len(page.Records) != 1 || page.Records[0].ID != record.ID {
		t.Fatalf("records with p-old trashed = %+v", page.Records)
	}
	f.photos.trash("p-park-1", true)
	if got := must(f.store.Record(ctx, mom, record.ID)); len(got.Photos) != 1 || got.Photos[0].AssetID != "p-park-2" {
		t.Fatalf("record with one photo trashed = %+v", got.Photos)
	}
	f.photos.trash("p-old", false)
	if page := must(f.store.Records(ctx, mom, baby.ID, "", "")); len(page.Records) != 2 {
		t.Fatalf("after restoring p-old: %+v", page.Records)
	}
	// Without the photo service nothing that needs photos is written.
	f.photos.down = true
	_, err = f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"p-park-2"}})
	wantErr(t, err, ErrPhotosUnavailable)
	// Reading goes on with what the channel stored; pages show placeholders
	// until the photo service answers again.
	stored := 0
	for _, record := range must(f.store.Records(ctx, mom, baby.ID, "", "")).Records {
		stored += len(record.Photos)
	}
	if stored != 3 {
		t.Fatalf("photos listed while the photo service is down = %d, want the 3 stored", stored)
	}
	if album := must(f.store.Album(ctx, mom, baby.ID, "")); len(album.Photos) != 3 {
		t.Fatalf("album while the photo service is down = %+v", album.Photos)
	}
}

func TestTheCloudAlbumGroupsByMonthOfLife(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	second := must(f.store.AddBaby(ctx, mom, BabyFields{Name: "二宝", Sex: "male", BirthDate: "2026-10-01"}))
	f.photos.add("ultrasound", at("2026-01-10 09:00"), true)
	f.photos.add("newborn", at("2026-03-16 09:00"), true)
	f.photos.add("both", at("2026-10-20 09:00"), true)
	f.photos.add("proof", at("2026-05-15 09:00"), true)
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"ultrasound", "newborn"}}))
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindPhotos, Photos: []string{"both"}, AlsoBabies: []string{second.ID}}))
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindVaccine, OccurredOn: "2026-05-15",
		Vaccine: &VaccineDose{Code: "dtap", Dose: 1}, Certificates: []string{"proof"}}))
	album := must(f.store.Album(ctx, dad, baby.ID, ""))
	var got []string
	for _, photo := range album.Photos {
		got = append(got, photo.AssetID+":"+itoa(photo.MonthOfLife))
	}
	if want := []string{"both:7", "newborn:0", "ultrasound:-1"}; !slices.Equal(got, want) {
		t.Fatalf("album = %v, want %v", got, want)
	}
	if other := must(f.store.Album(ctx, dad, second.ID, "")); len(other.Photos) != 1 || other.Photos[0].MonthOfLife != 0 {
		t.Fatalf("second baby's album = %+v", other.Photos)
	}
}

func itoa(value int) string {
	if value < 0 {
		return "-" + itoa(-value)
	}
	if value < 10 {
		return string(rune('0' + value))
	}
	return itoa(value/10) + itoa(value%10)
}

func TestAuthorsEditAndGuardiansDelete(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	weight := 7.4
	record := must(f.store.AddRecord(ctx, grandma, baby.ID, RecordInput{Kind: KindMeasurement, OccurredOn: "2026-09-15",
		Measurement: &Measurement{HeightCm: ptr(66.0), WeightKg: &weight}}))
	fixed := 7.2
	_, err := f.store.UpdateRecord(ctx, mom, record.ID, RecordInput{OccurredOn: "2026-09-15", Version: record.Version,
		Measurement: &Measurement{HeightCm: ptr(66.0), WeightKg: &fixed}})
	wantErr(t, err, ErrForbidden)
	updated := must(f.store.UpdateRecord(ctx, grandma, record.ID, RecordInput{OccurredOn: "2026-09-15", Version: record.Version,
		Measurement: &Measurement{HeightCm: ptr(66.0), WeightKg: &fixed}}))
	if *updated.Measurement.WeightKg != 7.2 || updated.Version != 2 {
		t.Fatalf("updated = %+v", updated.Measurement)
	}
	_, err = f.store.UpdateRecord(ctx, grandma, record.ID, RecordInput{OccurredOn: "2026-09-15", Version: record.Version,
		Measurement: &Measurement{WeightKg: &fixed}})
	wantErr(t, err, ErrChanged)
	// Measurements fall in the baby's life and in sensible ranges.
	for _, input := range []RecordInput{
		{Kind: KindMeasurement, OccurredOn: "2026-03-01", Measurement: &Measurement{WeightKg: &fixed}},
		{Kind: KindMeasurement, OccurredOn: "2026-11-01", Measurement: &Measurement{WeightKg: &fixed}},
		{Kind: KindMeasurement, OccurredOn: "2026-09-01", Measurement: &Measurement{HeadCm: ptr(90.0)}},
		{Kind: KindMeasurement, OccurredOn: "2026-09-01", Measurement: &Measurement{}},
	} {
		_, err := f.store.AddRecord(ctx, grandma, baby.ID, input)
		wantErr(t, err, ErrInvalid)
	}
	comment := must(f.store.Comment(ctx, dad, record.ID, CommentInput{Text: "长得真快"}))
	wantErr(t, f.store.DeleteRecord(ctx, dad, record.ID), nil)
	trash := must(f.store.Trash(ctx, grandma, baby.ID))
	if len(trash) != 1 || trash[0].ID != record.ID {
		t.Fatalf("grandma's trash = %+v", trash)
	}
	restored := must(f.store.RestoreRecord(ctx, grandma, record.ID))
	if len(restored.Comments) != 1 || restored.Comments[0].ID != comment.ID {
		t.Fatalf("restored = %+v", restored.Comments)
	}
	wantErr(t, f.store.DeleteComment(ctx, grandma, comment.ID), ErrForbidden)
	wantErr(t, f.store.DeleteComment(ctx, mom, comment.ID), nil)
	// After the trash period the record is gone for good.
	wantErr(t, f.store.DeleteRecord(ctx, grandma, record.ID), nil)
	f.now = f.now.AddDate(0, 0, TrashDays+1)
	if err := f.store.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.store.RestoreRecord(ctx, grandma, record.ID)
	wantErr(t, err, ErrNotFound)
}

func TestGrowthUsesTheChineseStandard(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	// WS/T 423—2022, girls' weight-for-age at birth: P25 3.1, P50 3.3.
	girls := must(f.store.AddBaby(ctx, mom, BabyFields{Name: "新生", Sex: "female", BirthDate: "2026-10-29"}))
	must(f.store.AddRecord(ctx, mom, girls.ID, RecordInput{Kind: KindMeasurement, OccurredOn: "2026-10-29",
		Measurement: &Measurement{WeightKg: ptr(3.2), HeadCm: ptr(29.0)}}))
	growth := must(f.store.Growth(ctx, mom, girls.ID))
	if w := growth.Latest.Weight; w == nil || w.Lower != "P25" || w.Upper != "P50" {
		t.Fatalf("weight at birth = %+v", w)
	}
	if h := growth.Latest.Head; h == nil || h.Lower != "" || h.Upper != "P3" {
		t.Fatalf("a small head at birth lies below P3, got %+v", h)
	}
	if !slices.Equal(growth.Percentiles, []string{"P3", "P10", "P25", "P50", "P75", "P90", "P97"}) {
		t.Fatalf("percentiles = %v", growth.Percentiles)
	}
	// Between sampled ages the curves are interpolated.
	values, ok := growthStandard.at("female", IndicatorWeight, 0.5)
	if !ok || math.Abs(values[3]-3.8) > 1e-9 {
		t.Fatalf("P50 at half a month = %v", values)
	}
	if _, ok := growthStandard.at("female", IndicatorHead, 40); ok {
		t.Fatal("head circumference has no reference after 36 months")
	}
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindMeasurement, OccurredOn: "2026-09-15",
		Measurement: &Measurement{HeightCm: ptr(66.0)}}))
	if growth := must(f.store.Growth(ctx, mom, baby.ID)); len(growth.Measurements) != 1 || growth.Measurements[0].AgeMonths != 6 {
		t.Fatalf("measurements = %+v", growth.Measurements)
	}
}

func TestVaccineScheduleFollowsThe2026Programme(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	statuses := func() map[string]ScheduledDose {
		schedule := must(f.store.Vaccines(ctx, mom, baby.ID))
		found := map[string]ScheduledDose{}
		for _, dose := range schedule.Doses {
			found[dose.Code+"-"+itoa(dose.Dose)] = dose
		}
		return found
	}
	doses := statuses()
	if dose := doses["dtap-1"]; dose.DueDate != "2026-05-15" || dose.Status != DoseAvailable {
		t.Fatalf("DTaP dose 1 is due at 2 months: %+v", dose)
	}
	if dose := doses["mmr-1"]; dose.DueDate != "2026-11-15" || dose.Status != DoseSoon {
		t.Fatalf("MMR dose 1 comes up within 30 days: %+v", dose)
	}
	if dose := doses["hpv-1"]; dose.Status != DoseUpcoming || dose.DueDate != "2039-03-15" {
		t.Fatalf("girls get HPV at 13: %+v", dose)
	}
	if _, ok := doses["je-i-1"]; ok || doses["je-l-1"].Code == "" {
		t.Fatal("the live JE vaccine is the default of its group")
	}
	// 五联 stands in for the same doses of DTaP and polio.
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindVaccine, OccurredOn: "2026-05-16",
		Vaccine: &VaccineDose{Code: "dtap-ipv-hib", Dose: 1, Clinic: "社区医院"}}))
	// A dose of the inactivated JE vaccine picks that schedule.
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindVaccine, OccurredOn: "2026-10-20",
		Vaccine: &VaccineDose{Code: "je-i", Dose: 1}}))
	// A conjugate meningococcal vaccine covers only the first two doses.
	for dose := 1; dose <= 3; dose++ {
		must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindVaccine, OccurredOn: "2026-09-01",
			Vaccine: &VaccineDose{Code: "men-ac-conj", Dose: dose}}))
	}
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindVaccine, OccurredOn: "2026-10-01",
		Vaccine: &VaccineDose{Code: CustomVaccine, Name: "某地方疫苗", Dose: 1}}))
	doses = statuses()
	for _, key := range []string{"dtap-1", "polio-1"} {
		if dose := doses[key]; dose.Status != DoseReplaced || dose.ReplacedBy == "" || dose.GivenOn != "2026-05-16" {
			t.Fatalf("%s after 五联 = %+v", key, dose)
		}
	}
	if dose := doses["dtap-2"]; dose.Status != DoseAvailable {
		t.Fatalf("DTaP dose 2 stays due: %+v", dose)
	}
	if _, ok := doses["je-l-1"]; ok || doses["je-i-2"].Status != DoseSoon {
		t.Fatal("recording the inactivated JE vaccine switches the schedule to it")
	}
	if doses["men-2"].Status != DoseReplaced || doses["men-3"].Status != DoseUpcoming {
		t.Fatalf("men after the conjugate vaccine: %+v / %+v", doses["men-2"], doses["men-3"])
	}
	// Families plan non-programme vaccines, or pick the other schedule of a
	// group; programme vaccines are always listed.
	_, err := f.store.SetVaccinePlan(ctx, grandma, baby.ID, []string{"dtap"})
	wantErr(t, err, ErrInvalid)
	plan := must(f.store.SetVaccinePlan(ctx, grandma, baby.ID, []string{"pcv13", "hepa-i"}))
	if !slices.Equal(plan.Plan, []string{"hepa-i", "pcv13"}) {
		t.Fatalf("plan = %v", plan.Plan)
	}
	doses = statuses()
	if doses["pcv13-1"].Status != DoseAvailable || doses["hepa-i-2"].Code == "" || doses["hepa-l-1"].Code != "" {
		t.Fatal("the plan adds PCV13 and switches hepatitis A to the inactivated vaccine")
	}
	_, err = f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindVaccine, OccurredOn: "2026-10-01",
		Vaccine: &VaccineDose{Code: "unknown", Dose: 1}})
	wantErr(t, err, ErrInvalid)
}

func TestHintsAreToldOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	if hints := must(f.store.Hints(ctx, tenant)); len(hints) != 0 {
		t.Fatalf("an account outside the family is told %+v", hints)
	}
	must(f.store.Join(ctx, tenant, "舅舅"))
	hints := must(f.store.Hints(ctx, mom))
	var keys []string
	joins := 0
	for _, hint := range hints {
		keys = append(keys, hint.Key)
		if hint.Kind == HintJoinRequest {
			joins++
		} else if hint.BabyID != baby.ID || hint.Vaccine == "" {
			t.Fatalf("hint = %+v", hint)
		} else if hint.DueDate < "2026-08-30" {
			t.Fatalf("a dose due long ago is backlog, not news: %+v", hint)
		}
	}
	if joins != 1 || len(hints) < 5 {
		t.Fatalf("mom's hints = %+v", hints)
	}
	for _, hint := range must(f.store.Hints(ctx, grandma)) {
		if hint.Kind == HintJoinRequest {
			t.Fatal("only guardians hear of join requests")
		}
	}
	if err := f.store.MarkHintsSeen(ctx, mom, keys); err != nil {
		t.Fatal(err)
	}
	if hints := must(f.store.Hints(ctx, mom)); len(hints) != 0 {
		t.Fatalf("told again: %+v", hints)
	}
	if hints := must(f.store.Hints(ctx, grandma)); len(hints) == 0 {
		t.Fatal("seen is per account")
	}
}

func TestAgesCountWholeMonthsLikeFamiliesDo(t *testing.T) {
	date := func(value string) time.Time {
		parsed, ok := parseDate(value)
		if !ok {
			t.Fatalf("bad date %s", value)
		}
		return parsed
	}
	for _, c := range []struct {
		birth, on string
		want      Age
	}{
		{"2026-03-15", "2026-03-15", Age{0, 0, 0, 0}},
		{"2026-03-15", "2026-04-14", Age{0, 0, 30, 0}},
		{"2026-03-15", "2026-04-15", Age{0, 1, 0, 1}},
		{"2026-01-31", "2026-02-28", Age{0, 1, 0, 1}},
		{"2026-01-31", "2026-03-30", Age{0, 1, 30, 1}},
		{"2024-02-29", "2025-02-28", Age{1, 0, 0, 12}},
		{"2025-03-15", "2026-05-17", Age{1, 2, 2, 14}},
	} {
		got, ok := ageOn(date(c.birth), date(c.on))
		if !ok || got != c.want {
			t.Errorf("age of %s on %s = %+v, want %+v", c.birth, c.on, got, c.want)
		}
	}
	if _, ok := ageOn(date("2026-03-15"), date("2026-03-14")); ok {
		t.Error("no age before birth")
	}
	birth := date("2026-03-15")
	for key, want := range map[string]string{"birth": "2026-03-15", "month-1": "2026-04-15", "day-100": "2026-06-22", "year-1": "2027-03-15", "birthday-3": "2029-03-15"} {
		milestone, ok := autoMilestone(birth, key)
		if !ok || milestone.Date.Format(dateLayout) != want {
			t.Errorf("%s = %v, want %s", key, milestone.Date, want)
		}
	}
	for _, key := range []string{"birthday-1", "birthday-02", "month-2", ""} {
		if _, ok := autoMilestone(birth, key); ok {
			t.Errorf("%q is not an automatic milestone", key)
		}
	}
}

func TestTheTimelineHoldsAutomaticAndFamilyMilestones(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	f.photos.add("hundred", at("2026-06-22 12:00"), true)
	must(f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindMilestone, Photos: []string{"hundred"},
		Milestone: &MilestoneFields{AutoKey: "day-100"}, Text: "百天照"}))
	_, err := f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindMilestone, Milestone: &MilestoneFields{AutoKey: "day-100"}})
	wantErr(t, err, ErrConflict)
	_, err = f.store.AddRecord(ctx, dad, baby.ID, RecordInput{Kind: KindMilestone, Milestone: &MilestoneFields{AutoKey: "year-1"}})
	wantErr(t, err, ErrInvalid)
	must(f.store.AddRecord(ctx, mom, baby.ID, RecordInput{Kind: KindMilestone, OccurredOn: "2026-07-01",
		Milestone: &MilestoneFields{Title: "第一次翻身"}}))
	entries := must(f.store.Timeline(ctx, grandma, baby.ID))
	var got []string
	for _, entry := range entries {
		label := entry.Date + " " + entry.Title
		if entry.Record != nil {
			label += " +"
		}
		if entry.Upcoming {
			label += " (upcoming)"
		}
		got = append(got, label)
	}
	want := []string{"2026-03-15 出生", "2026-04-15 满月", "2026-06-22 百天 +", "2026-07-01 第一次翻身 +", "2027-03-15 周岁 (upcoming)"}
	if !slices.Equal(got, want) {
		t.Fatalf("timeline = %q\nwant       %q", got, want)
	}
}

func TestRecordingsAreStoredWholeOrNotAtAll(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	baby := f.family(t)
	_, err := f.store.AddRecording(ctx, grandma, baby.ID, RecordingInput{ContentType: "video/mp4"}, strings.NewReader("x"))
	wantErr(t, err, ErrInvalid)
	_, err = f.store.AddRecording(ctx, tenant, baby.ID, RecordingInput{ContentType: "audio/mp4"}, strings.NewReader("x"))
	wantErr(t, err, ErrNotMember)
	record := must(f.store.AddRecording(ctx, grandma, baby.ID, RecordingInput{
		RequestKey: "r1", Title: "唱儿歌", FileName: `C:\录音\儿歌.m4a`, ContentType: "audio/x-m4a",
	}, strings.NewReader("audio bytes")))
	if record.Kind != KindRecording || record.Recording.FileName != "儿歌.m4a" || record.Recording.SizeBytes != 11 || record.OccurredOn != "2026-10-29" {
		t.Fatalf("recording = %+v %+v", record, record.Recording)
	}
	again := must(f.store.AddRecording(ctx, grandma, baby.ID, RecordingInput{RequestKey: "r1", ContentType: "audio/x-m4a"}, strings.NewReader("again")))
	if again.ID != record.ID {
		t.Fatal("a retried upload added a second recording")
	}
	file, recording, err := f.store.OpenRecording(ctx, dad, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	if recording.ContentType != "audio/x-m4a" || NeedsConversion(recording.ContentType) || !NeedsConversion("audio/amr") {
		t.Fatalf("recording = %+v", recording)
	}
	entries, _ := os.ReadDir(filepath.Join(f.dir, "recordings"))
	if len(entries) != 1 {
		t.Fatalf("recordings on disk = %v", entries)
	}
	wantErr(t, f.store.DeleteRecord(ctx, grandma, record.ID), nil)
	f.now = f.now.AddDate(0, 0, TrashDays+1)
	if err := f.store.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dir, "recordings")); len(entries) != 0 {
		t.Fatalf("purged recording left %v", entries)
	}
}

func TestTheCatalogIsWellFormed(t *testing.T) {
	catalog := CurrentCatalog()
	if catalog.Vaccines.Version != "2026" || catalog.GrowthStandard == "" || len(catalog.Roles) == 0 {
		t.Fatalf("catalog = %+v", catalog)
	}
	for _, code := range []string{"hepb", "bcg", "polio", "dtap", "mmr", "je-l", "je-i", "men", "hepa-l", "hepa-i", "hpv"} {
		if vaccine, ok := vaccineByCode(code); !ok || !vaccine.Programme {
			t.Errorf("%s is a programme vaccine", code)
		}
	}
	dtap, _ := vaccineByCode("dtap")
	var months []int
	for _, dose := range dtap.Doses {
		months = append(months, dose.AgeMonths)
	}
	if !slices.Equal(months, []int{2, 4, 6, 18, 72}) {
		t.Fatalf("DTaP is given at 2, 4, 6 and 18 months and 6 years since 2025, got %v", months)
	}
}
