package photos_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/ffmpegworker"
	"github.com/zhongwater123/A-NAS/internal/photos"
)

const describeJob = "montage-describe/v1"

// describingEmbedder is the reading Worker that also describes montage
// cards: answer, when set, says what a card gets from the frames' count and
// the call's order; otherwise a description holding numbers to mask.
type describingEmbedder struct {
	*readingEmbedder
	dmu      sync.Mutex
	layouts  []photos.CardLayout
	frames   []int
	answer   func(call int, card photos.CardLayout) (json.RawMessage, error)
	template string
	// blockDescribing makes a description wait until it is cancelled, after
	// saying so on describing.
	blockDescribing bool
	describing      chan struct{}
	infos           int
	// embedding, when set, is the image model in place of colour-1: a new
	// one makes the windows again, and with them the cards.
	embedding string
}

func (d *describingEmbedder) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	d.dmu.Lock()
	d.infos++
	embedding := d.embedding
	d.dmu.Unlock()
	info, err := d.readingEmbedder.Info(ctx)
	if embedding != "" {
		info.Model = embedding
	}
	return info, err
}

// EmbedImage derives a unit vector from the bytes, so the windows' black
// frames are encoded too, as the real model encodes them.
func (d *describingEmbedder) EmbedImage(_ context.Context, file *os.File) ([]float32, error) {
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(content)
	return unit(float64(sum[0])+1, float64(sum[1])+1, float64(sum[2])+1), nil
}

func (d *describingEmbedder) infoCount() int {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	return d.infos
}

func newDescribingEmbedder() *describingEmbedder {
	return &describingEmbedder{readingEmbedder: newReadingEmbedder(), template: "montage-card/v2"}
}

// numbersAnswer holds an ID number, a card number, a mobile number and a
// long digit string in the Worker's answer, and a short ticket number.
const numbersAnswer = `{"camera":"地面","beats":[{"shot_size":"近景","what":"证件 11010519491231002X 放在桌上","frames":[1,1]}],` +
	`"process":["办理入住"],"summary":"刷卡 4111 1111 1111 1111，电话 13812345678，单号 123456789012345678901，座位 12A",` +
	`"scene":"酒店前台"}`

func (d *describingEmbedder) DescribeFrames(ctx context.Context, frames []*os.File, card photos.CardLayout) (string, string, json.RawMessage, error) {
	for _, frame := range frames {
		if content, err := io.ReadAll(frame); err != nil || len(content) == 0 {
			return "", "", nil, fmt.Errorf("a key frame could not be read: %v", err)
		}
	}
	d.dmu.Lock()
	d.layouts = append(d.layouts, card)
	d.frames = append(d.frames, len(frames))
	call, answer, template, block, describing := len(d.layouts), d.answer, d.template, d.blockDescribing, d.describing
	d.dmu.Unlock()
	d.mu.Lock()
	model := d.model
	d.mu.Unlock()
	if block {
		describing <- struct{}{}
		<-ctx.Done()
		return "", "", nil, ctx.Err()
	}
	if answer == nil {
		return model, template, json.RawMessage(numbersAnswer), nil
	}
	description, err := answer(call, card)
	return model, template, description, err
}

func (d *describingEmbedder) calls() int {
	d.dmu.Lock()
	defer d.dmu.Unlock()
	return len(d.layouts)
}

type describeFixture struct {
	montageFixture
	embedder *describingEmbedder
	video    photos.Asset
	object   string
	grant    photos.Grant
}

// newDescribeFixture grants and analyses a 70-second video of Alice's
// through the whole chain, windows, cards, quality and, with a text reader,
// descriptions; then it sets the cards a test describes: card 0 and 2 pass
// the quality check, card 1 is blurred.
func newDescribeFixture(t *testing.T) describeFixture {
	t.Helper()
	return newDescribeFixtureWith(t, "reader-1")
}

func newDescribeFixtureWith(t *testing.T, reader string) describeFixture {
	t.Helper()
	ctx := context.Background()
	f := describeFixture{montageFixture: newMontageFixture(t), embedder: newDescribingEmbedder()}
	f.enableCompute(t)
	f.embedder.model = reader
	private, _ := libraries(t, f.service, namedAlice)
	f.video = importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	grant, err := f.service.RegisterGrant(ctx, granting(namedAlice), "project:1", []string{f.video.ID})
	if err != nil {
		t.Fatal(err)
	}
	f.grant = grant
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), grant.ID, []string{f.video.ID}, false); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	f.object = f.lookup(t).ObjectID
	if count, _ := f.windows(t, f.object); count != 35 {
		t.Fatalf("windows = %d", count)
	}
	// The compute Worker's two cards, both good, are described once their
	// quality is measured, or wait for a reader.
	if want := map[bool]int{true: 2, false: 0}[reader != ""]; len(f.descriptions(t)) != want || f.embedder.calls() != want {
		t.Fatalf("after the chain: %d descriptions, %d calls, want %d", len(f.descriptions(t)), f.embedder.calls(), want)
	}
	// The beats the compute Worker gave reach the description.
	for _, layout := range f.embedder.layouts {
		if !slices.Equal(layout.Groups, []int{1, 1, 2}) {
			t.Fatalf("layout after the chain = %+v, want the compute Worker's beats", layout)
		}
	}
	f.cards(t, []standInCard{
		{start: 0, end: 20000, keyframes: []float64{1, 8, 15}, reasons: "[]"},
		{start: 20000, end: 40000, keyframes: []float64{21, 30, 39}, reasons: `["模糊"]`},
		{start: 40000, end: 70000, keyframes: []float64{41, 50, 60, 69.99}, groups: []int{1, 1, 2, 2}, reasons: "[]"},
	})
	return f
}

type standInCard struct {
	start, end int
	keyframes  []float64
	groups     []int // nil: cut before cards had beats
	reasons    string
}

// cards replaces the cards and quality the compute Worker made with these,
// key frames in seconds as it stores them, and queues their descriptions as
// the quality step's end does.
func (f describeFixture) cards(t *testing.T, cards []standInCard) {
	t.Helper()
	db := catalog(t, f.root)
	f.embedder.dmu.Lock()
	f.embedder.layouts, f.embedder.frames = nil, nil
	f.embedder.dmu.Unlock()
	for _, statement := range []string{`DELETE FROM video_cards`, `DELETE FROM video_card_quality`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var cardsID, qualityID string
	if err := db.QueryRow("SELECT cards_identity,quality_identity FROM montage_card_results WHERE object_id=?", f.object).Scan(&cardsID, &qualityID); err != nil {
		t.Fatal(err)
	}
	for i, card := range cards {
		keyframes, _ := json.Marshal(card.keyframes)
		beats := ""
		if card.groups != nil {
			grouped, _ := json.Marshal(card.groups)
			beats = string(grouped)
		}
		if _, err := db.Exec("INSERT INTO video_cards(object_id, card, start_ms, end_ms, keyframes, beats, identity) VALUES(?, ?, ?, ?, ?, ?, ?)",
			f.object, i, card.start, card.end, string(keyframes), beats, cardsID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO video_card_quality VALUES(?, ?, ?, ?)", f.object, i, `{"blur":0.1,"reasons":`+card.reasons+`}`, qualityID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("DELETE FROM video_card_descriptions WHERE object_id = ?", f.object); err != nil {
		t.Fatal(err)
	}
	if err := f.service.QueueDescribe(context.Background(), f.object); err != nil {
		t.Fatal(err)
	}
}

type storedDescription struct {
	card     int
	result   string
	failure  string
	identity string
	release  string
}

func (f describeFixture) descriptions(t *testing.T) []storedDescription {
	t.Helper()
	rows, err := catalog(t, f.root).Query("SELECT card, IFNULL(result, ''), error, identity, IFNULL(written_release, '') FROM video_card_descriptions ORDER BY card")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []storedDescription
	for rows.Next() {
		var d storedDescription
		if err := rows.Scan(&d.card, &d.result, &d.failure, &d.identity, &d.release); err != nil {
			t.Fatal(err)
		}
		found = append(found, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func (f describeFixture) lookup(t *testing.T) photos.GrantedAsset {
	t.Helper()
	looked, err := f.service.LookupGrant(context.Background(), granting(namedAlice), f.grant.ID, []string{f.video.ID})
	if err != nil || len(looked) != 1 {
		t.Fatalf("LookupGrant() = %+v, %v", looked, err)
	}
	return looked[0]
}

// Read through the descriptor just as the grant client does.
func (f describeFixture) analysis(t *testing.T) (photos.GrantAnalysis, error) {
	t.Helper()
	a, file, err := f.service.AnalysisOfGrant(t.Context(), granting(namedAlice), f.grant.ID, f.video.ID)
	if err != nil || file == nil {
		return a, err
	}
	defer func() { _ = file.Close() }()
	_, err = photos.ReadWindows(&a, file)
	return a, err
}

func TestCardsThatPassedTheQualityCheckAreDescribedWithoutTheirNumbers(t *testing.T) {
	f := newDescribeFixture(t)
	if state := f.lookup(t).Descriptions; state != photos.AnalysisQueued {
		t.Fatalf("descriptions before the round = %q", state)
	}
	// A card a round; the last one finishes the job.
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 {
		t.Fatalf("rounds = %d, want one for each card that passed", rounds)
	}
	if f.embedder.calls() != 2 || !slices.Equal(f.embedder.frames, []int{3, 4}) {
		t.Fatalf("described %d cards with %v frames; the blurred card must be left out", f.embedder.calls(), f.embedder.frames)
	}
	// Seconds from the card's start, the last frame before the video's
	// end, in the beats the cards gave; a card without beats has a beat a
	// frame.
	last := f.embedder.layouts[1]
	if !slices.Equal(last.Offsets, []float64{1, 10, 20, 29.99}) || !slices.Equal(last.Groups, []int{1, 1, 2, 2}) || last.Duration != 30 {
		t.Fatalf("layout = %+v", last)
	}
	if first := f.embedder.layouts[0]; !slices.Equal(first.Groups, []int{1, 2, 3}) {
		t.Fatalf("layout without beats = %+v", first)
	}
	stored := f.descriptions(t)
	if len(stored) != 2 || stored[0].card != 0 || stored[1].card != 2 {
		t.Fatalf("stored descriptions = %+v", stored)
	}
	for _, number := range []string{"11010519491231002X", "4111 1111 1111 1111", "13812345678", "123456789012345678901"} {
		if strings.Contains(stored[0].result, number) {
			t.Fatalf("the stored description keeps %s: %s", number, stored[0].result)
		}
	}
	for _, label := range []string{"证件 [身份证号] 放在桌上", "刷卡 [卡号]", "电话 [手机号]", "单号 [号码]", "座位 12A"} {
		if !strings.Contains(stored[0].result, label) {
			t.Fatalf("the stored description lacks %q: %s", label, stored[0].result)
		}
	}
	if !strings.HasPrefix(stored[0].identity, "reader-1|tpl=montage-card/v2,genre=TRAVEL,") || !strings.Contains(stored[0].identity, "|quality=montage-quality/v1") {
		t.Fatalf("identity = %q", stored[0].identity)
	}
	if f.montageJobs(t) != 0 {
		t.Fatalf("montage jobs left = %d", f.montageJobs(t))
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("descriptions after the rounds = %q", state)
	}
	analysis, err := f.analysis(t)
	if err != nil || analysis.Described != photos.AnalysisDone || len(analysis.Descriptions) != 2 || analysis.Descriptions[1].Card != 2 {
		t.Fatalf("AnalysisOfGrant() = %+v, %v", analysis, err)
	}
	var description struct {
		Summary string `json:"summary"`
		Beats   []struct {
			Frames []int `json:"frames"`
		} `json:"beats"`
	}
	if err := json.Unmarshal(analysis.Descriptions[0].Description, &description); err != nil ||
		!strings.Contains(description.Summary, "[手机号]") || !slices.Equal(description.Beats[0].Frames, []int{1, 1}) {
		t.Fatalf("description = %s, %v", analysis.Descriptions[0].Description, err)
	}
	// Asking again finds them done.
	if err := f.service.AnalyzeGrant(context.Background(), granting(namedAlice), f.grant.ID, []string{f.video.ID}, false); err != nil {
		t.Fatal(err)
	}
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 2 {
		t.Fatalf("asking again ran %d rounds, %d descriptions", rounds, f.embedder.calls())
	}

	// A new reader, which comes with a release and so a restart, describes
	// again what a grant still refers to.
	f.embedder.mu.Lock()
	f.embedder.model = "reader-2"
	f.embedder.mu.Unlock()
	_ = f.service.Close()
	service, err := photos.Open(f.root, photos.Options{Now: f.clock.Now, Media: f.montageFixture.video, Compute: f.compute, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || f.embedder.calls() != 4 {
		t.Fatalf("a new reader ran %d rounds, %d descriptions", rounds, f.embedder.calls())
	}
	if stored := f.descriptions(t); !strings.HasPrefix(stored[1].identity, "reader-2|") {
		t.Fatalf("identity after the new reader = %q", stored[1].identity)
	}
}

func TestACardThatCannotBeDescribedLeavesTheOthers(t *testing.T) {
	f := newDescribeFixture(t)
	f.cards(t, []standInCard{
		{start: 0, end: 10000, keyframes: []float64{1, 5, 9}, reasons: "[]"},
		{start: 10000, end: 20000, keyframes: []float64{11, 15, 19}, reasons: "[]"},
		{start: 20000, end: 30000, keyframes: []float64{21, 25, 29}, reasons: "[]"},
	})
	f.embedder.answer = func(call int, _ photos.CardLayout) (json.RawMessage, error) {
		switch call {
		case 1:
			return nil, fmt.Errorf("%w: llama-server answered 400: the request exceeds the available context size", photos.ErrAIRejected)
		case 2:
			return nil, fmt.Errorf("%w: repeat_penalty=1.3: answer lacks scene", photos.ErrAIInvalidResult)
		}
		return json.RawMessage(`{"camera":"地面","summary":"湖边"}`), nil
	}
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 3 {
		t.Fatalf("rounds = %d", rounds)
	}
	stored := f.descriptions(t)
	if len(stored) != 3 || stored[0].result != "" || !strings.Contains(stored[0].failure, "context size") ||
		stored[1].result != "" || !strings.Contains(stored[1].failure, "lacks scene") ||
		stored[2].failure != "" || !strings.Contains(stored[2].result, "湖边") {
		t.Fatalf("stored = %+v", stored)
	}
	analysis, err := f.analysis(t)
	if err != nil || analysis.Described != photos.AnalysisDone || len(analysis.Descriptions) != 3 ||
		analysis.Descriptions[0].Description != nil || analysis.Descriptions[0].Error == "" || analysis.Descriptions[2].Description == nil {
		t.Fatalf("AnalysisOfGrant() = %+v, %v", analysis, err)
	}
}

func TestDescriptionsWaitForATextReader(t *testing.T) {
	// The Worker has no text reader: the quality step ends without queueing
	// the descriptions, and lookup says the local model is unavailable.
	f := newDescribeFixtureWith(t, "")
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 0 || f.montageJobs(t) != 0 {
		t.Fatalf("without a reader: %d rounds, %d descriptions, %d montage jobs", rounds, f.embedder.calls(), f.montageJobs(t))
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisUnavailable {
		t.Fatalf("descriptions without a reader = %q, want the local model unavailable", state)
	}
	// Nothing is due: the Worker is not asked again.
	infos := f.embedder.infoCount()
	processAI(t, f.service, f.embedder, f.gate)
	if f.embedder.infoCount() != infos {
		t.Fatalf("a round asked the Worker %d times with nothing it can do", f.embedder.infoCount()-infos)
	}

	// A reader comes with a release, and so a restart: the descriptions are
	// queued and written.
	f.embedder.mu.Lock()
	f.embedder.model = "reader-1"
	f.embedder.mu.Unlock()
	_ = f.service.Close()
	service, err := photos.Open(f.root, photos.Options{Now: f.clock.Now, Media: f.montageFixture.video, Compute: f.compute, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || len(f.descriptions(t)) != 2 {
		t.Fatalf("with a reader: %d rounds, %d descriptions", rounds, len(f.descriptions(t)))
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("descriptions with a reader = %q", state)
	}
}

func TestGrantReauthorizedAfterReaderChange(t *testing.T) {
	f := newDescribeFixture(t)
	ctx := t.Context()
	processAI(t, f.service, f.embedder, f.gate)
	if err := f.service.RevokeGrant(ctx, f.grant.ID); err != nil {
		t.Fatal(err)
	}
	f.embedder.mu.Lock()
	f.embedder.model = "reader-2"
	f.embedder.mu.Unlock()
	_ = f.service.Close()
	service, err := photos.Open(f.root, photos.Options{Now: f.clock.Now, Media: f.montageFixture.video, Compute: f.compute, DisableCapacityReserve: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	processAI(t, service, f.embedder, f.gate)
	f.grant, err = service.RegisterGrant(ctx, granting(namedAlice), "project:2", []string{f.video.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AnalyzeGrant(ctx, granting(namedAlice), f.grant.ID, []string{f.video.ID}, false); err != nil {
		t.Fatal(err)
	}
	processAI(t, service, f.embedder, f.gate)
	a, err := f.analysis(t)
	if err != nil || a.Described != photos.AnalysisDone || !strings.HasPrefix(a.DescriptionsIdentity, "reader-2|") || len(a.Descriptions) != 2 {
		t.Fatalf("reauthorized analysis: state %s, identity %s, %d descriptions, %v", a.Described, a.DescriptionsIdentity, len(a.Descriptions), err)
	}
}

func TestNoEligibleCardsNeedNoReader(t *testing.T) {
	f := newDescribeFixtureWith(t, "")
	f.cards(t, []standInCard{{start: 0, end: 70000, keyframes: []float64{1, 3, 5}, reasons: `["blur"]`}})
	a, err := f.analysis(t)
	if err != nil || a.Described != photos.AnalysisDone || a.DescriptionsIdentity == "" || len(a.Descriptions) != 0 {
		t.Fatalf("zero eligible cards: state %s, identity %q, %v", a.Described, a.DescriptionsIdentity, err)
	}
}

func TestSomeoneUsingTheLibraryHangsUpADescription(t *testing.T) {
	f := newDescribeFixture(t)
	f.embedder.blockDescribing, f.embedder.describing = true, make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := f.service.ProcessAIJob(context.Background(), f.embedder, f.gate)
		done <- err
	}()
	select {
	case <-f.embedder.describing:
	case <-time.After(10 * time.Second):
		t.Fatal("the description never started")
	}
	// A search starts: the gate closes, and the description is hung up
	// within the watch's 200 ms.
	f.gate.set(false, "foreground")
	started := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the description went on after someone started using the library")
	}
	if waited := time.Since(started); waited > 2*time.Second {
		t.Fatalf("the description took %v to stop", waited)
	}
	var state string
	var attempts int
	if err := catalog(t, f.root).QueryRow("SELECT state, attempts FROM jobs WHERE derivation = 'montage-describe/v1'").Scan(&state, &attempts); err != nil ||
		state != "pending" || attempts != 0 {
		t.Fatalf("the job after the hang-up: %s, %d attempts, %v", state, attempts, err)
	}
	if released := f.embedder.releases(); !slices.Contains(released, false) {
		t.Fatalf("releases = %v, want the reader released for the foreground", released)
	}
	// Once the library is quiet again, the description goes on.
	f.embedder.dmu.Lock()
	f.embedder.blockDescribing = false
	f.embedder.dmu.Unlock()
	f.gate.set(true, "")
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || len(f.descriptions(t)) != 2 {
		t.Fatalf("after the library went quiet: %d rounds, %d descriptions", rounds, len(f.descriptions(t)))
	}
}

func TestRetryStartsOnlyTheFailedCardOver(t *testing.T) {
	f := newDescribeFixture(t)
	f.embedder.answer = func(call int, _ photos.CardLayout) (json.RawMessage, error) {
		if call <= 3 {
			return nil, errors.New("the Worker died")
		}
		return json.RawMessage(numbersAnswer), nil
	}
	for range 3 {
		processAI(t, f.service, f.embedder, f.gate)
		f.clock.Advance(time.Hour)
	}
	if got := f.descriptions(t); len(got) != 2 || got[0].failure == "" || got[1].result == "" {
		t.Fatalf("after three crashes on the first card: %+v", got)
	}
	f.embedder.answer = nil
	if err := f.service.AnalyzeGrant(context.Background(), granting(namedAlice), f.grant.ID, []string{f.video.ID}, true); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("after the retry: %q", state)
	}
	if got := f.descriptions(t); len(got) != 2 || got[0].failure != "" || f.embedder.calls() != 5 {
		t.Fatalf("retry should only repeat the failed card: %+v, %d calls", got, f.embedder.calls())
	}
}

func TestDescriptionsGoWithTheirObject(t *testing.T) {
	f := newDescribeFixture(t)
	processAI(t, f.service, f.embedder, f.gate)
	if len(f.descriptions(t)) != 2 {
		t.Fatal("no descriptions to remove")
	}
	if err := f.service.RevokeGrant(context.Background(), f.grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PruneMontageAnalysis(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(8 * 24 * time.Hour)
	if _, err := f.service.PruneMontageAnalysis(context.Background()); err != nil {
		t.Fatal(err)
	}
	if left := f.descriptions(t); len(left) != 0 {
		t.Fatalf("descriptions left 7 days after the last grant went = %+v", left)
	}
}

func TestWaitingForReaderMemoryLetsPhotosEmbed(t *testing.T) {
	f := newDescribeFixture(t)
	private, _ := libraries(t, f.service, namedAlice)
	importPhoto(t, f.service, namedAlice, private.ID, "", "new.png", pngBytes(t, 37))
	processAll(t, f.service)
	processAI(t, f.service, f.embedder, &roomGate{room: 1 << 20})
	var vectors int
	if err := catalog(t, f.root).QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&vectors); err != nil || vectors != 1 {
		t.Fatalf("embeddings while reader waits for memory = %d, %v", vectors, err)
	}
	if f.embedder.calls() != 0 || f.lookup(t).Descriptions != photos.AnalysisQueued {
		t.Fatal("description should remain queued until the reader fits")
	}
}

func TestPurgingRunningDescriptionDiscardsLateResult(t *testing.T) {
	f := newDescribeFixture(t)
	ctx := t.Context()
	f.embedder.answer = func(_ int, _ photos.CardLayout) (json.RawMessage, error) {
		if err := f.service.RevokeGrant(ctx, f.grant.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.PruneMontageAnalysis(ctx); err != nil {
			t.Fatal(err)
		}
		f.clock.Advance(8 * 24 * time.Hour)
		if _, err := f.service.PruneMontageAnalysis(ctx); err != nil {
			t.Fatal(err)
		}
		return json.RawMessage(numbersAnswer), nil
	}
	processAI(t, f.service, f.embedder, f.gate)
	if got := f.descriptions(t); len(got) != 0 || f.montageJobs(t) != 0 {
		t.Fatalf("late result for purged video: %+v", got)
	}
}

// Cards cut again are described again, under the identity of what they
// were cut from: the old cards' descriptions go, and so does a description
// job that had failed, which would keep the new cards' from being queued.
func TestCardsCutAgainAreDescribedAgain(t *testing.T) {
	f := newDescribeFixture(t)
	// The first card is described; then the job fails, as one that a crash
	// interrupted three times does.
	if worked, err := f.service.ProcessAIJob(context.Background(), f.embedder, f.gate); !worked || err != nil {
		t.Fatalf("the first card's round = %v, %v", worked, err)
	}
	old := f.descriptions(t)
	if len(old) != 1 || old[0].card != 0 || !strings.Contains(old[0].identity, "colour-1") {
		t.Fatalf("descriptions before the job failed = %+v", old)
	}
	if _, err := catalog(t, f.root).Exec("UPDATE jobs SET state = 'failed', attempts = 3, error_class = 'interrupted' WHERE derivation = ?", describeJob); err != nil {
		t.Fatal(err)
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisFailed {
		t.Fatalf("descriptions with their job failed = %q", state)
	}
	// A new image model comes with a release, which restarts the photo
	// service: the windows are made again, the cards cut and measured again.
	f.embedder.dmu.Lock()
	f.embedder.embedding = "colour-2"
	f.embedder.dmu.Unlock()
	f.restart(t)
	for round := 1; f.count(t, "SELECT COUNT(*) FROM video_cards WHERE identity LIKE '%colour-2%'") == 0; round++ {
		if worked, err := f.service.ProcessAIJob(context.Background(), f.embedder, f.gate); !worked || err != nil || round > 10 {
			t.Fatalf("round %d before the cards were cut again = %v, %v", round, worked, err)
		}
	}
	// The failed job and the old cards' descriptions went with the old cards.
	if _, left := f.job(t, describeJob); left || len(f.descriptions(t)) != 0 {
		t.Fatalf("with the cards cut again: a description job left: %v, descriptions %+v", left, f.descriptions(t))
	}
	processAI(t, f.service, f.embedder, f.gate)
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone || f.montageJobs(t) != 0 {
		t.Fatalf("descriptions of the cards cut again = %q, %d montage jobs left", state, f.montageJobs(t))
	}
	// The compute stand-in's two cards, numbered from 1; the first round's
	// card 0 is gone with the cards it described.
	stored := f.descriptions(t)
	if len(stored) != 2 || stored[0].card != 1 || stored[1].card != 2 || f.embedder.calls() != 3 {
		t.Fatalf("stored = %+v after %d descriptions", stored, f.embedder.calls())
	}
	for _, d := range stored {
		if d.result == "" || !strings.HasPrefix(d.identity, "reader-1|") || !strings.Contains(d.identity, "colour-2") {
			t.Fatalf("a description of the cards cut again = %+v", d)
		}
	}
	analysis, err := f.analysis(t)
	if err != nil || analysis.Described != photos.AnalysisDone || len(analysis.Descriptions) != 2 ||
		!strings.HasSuffix(analysis.DescriptionsIdentity, "|quality="+analysis.QualityIdentity) ||
		!strings.Contains(analysis.QualityIdentity, "colour-2") {
		t.Fatalf("AnalysisOfGrant() = %+v, %v", analysis, err)
	}
}

// An answer under a template the library does not know comes from a Worker
// of another release: it is the card's failure, stored once, not a
// description asked for again every minute.
func TestAnAnswerUnderAnotherTemplateFailsTheCardOnce(t *testing.T) {
	f := newDescribeFixture(t)
	f.embedder.dmu.Lock()
	f.embedder.template = "montage-card/v1"
	f.embedder.dmu.Unlock()
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || f.embedder.calls() != 2 {
		t.Fatalf("%d rounds, %d descriptions; want one for each card", rounds, f.embedder.calls())
	}
	stored := f.descriptions(t)
	if len(stored) != 2 || f.montageJobs(t) != 0 {
		t.Fatalf("stored = %+v, %d montage jobs left", stored, f.montageJobs(t))
	}
	for _, d := range stored {
		if d.result != "" || !strings.Contains(d.failure, "template") {
			t.Fatalf("a description under another template = %+v", d)
		}
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("descriptions = %q", state)
	}
	f.clock.Advance(time.Hour)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 2 {
		t.Fatalf("an hour later: %d rounds, %d descriptions in all", rounds, f.embedder.calls())
	}
}

// A description job that failed stays failed over a restart, unlike other
// jobs that failed on storage: a try can take the Worker a quarter of an
// hour. A reader the library has not seen before tries it again.
func TestAFailedDescriptionJobWaitsForANewReaderNotARestart(t *testing.T) {
	f := newDescribeFixture(t)
	if _, err := catalog(t, f.root).Exec("UPDATE jobs SET state = 'failed', attempts = 3, error_class = 'io' WHERE derivation = ?", describeJob); err != nil {
		t.Fatal(err)
	}
	f.restart(t)
	if recovery, err := f.service.RecoverJobs(context.Background()); err != nil || recovery.Requeued != 0 {
		t.Fatalf("RecoverJobs() = %+v, %v; want the description job left failed", recovery, err)
	}
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 0 {
		t.Fatalf("after a restart with the same reader: %d rounds, %d descriptions", rounds, f.embedder.calls())
	}
	f.wantJob(t, describeJob, jobRow{state: "failed", class: "io", attempts: 3})
	if state := f.lookup(t).Descriptions; state != photos.AnalysisFailed {
		t.Fatalf("descriptions after the restart = %q", state)
	}

	f.embedder.mu.Lock()
	f.embedder.model = "reader-2"
	f.embedder.mu.Unlock()
	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || f.montageJobs(t) != 0 {
		t.Fatalf("with a new reader: %d rounds, %d montage jobs left", rounds, f.montageJobs(t))
	}
	stored := f.descriptions(t)
	if len(stored) != 2 || !strings.HasPrefix(stored[0].identity, "reader-2|") || !strings.HasPrefix(stored[1].identity, "reader-2|") {
		t.Fatalf("stored with the new reader = %+v", stored)
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("descriptions with the new reader = %q", state)
	}
}

// A description the number filter cannot read is that card's failure: the
// video's other cards are described.
func TestADescriptionTheNumberFilterCannotReadLeavesTheOthers(t *testing.T) {
	f := newDescribeFixture(t)
	f.embedder.answer = func(call int, _ photos.CardLayout) (json.RawMessage, error) {
		if call == 1 {
			return json.RawMessage(`{"camera":"地面","summary":`), nil
		}
		return json.RawMessage(numbersAnswer), nil
	}
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 {
		t.Fatalf("rounds = %d", rounds)
	}
	stored := f.descriptions(t)
	if len(stored) != 2 || stored[0].result != "" || stored[0].failure != "invalid_result" ||
		stored[1].failure != "" || !strings.Contains(stored[1].result, "[手机号]") {
		t.Fatalf("stored = %+v", stored)
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone || f.montageJobs(t) != 0 {
		t.Fatalf("descriptions = %q, %d montage jobs left", state, f.montageJobs(t))
	}
}

// damagedVideo cannot hand over the key frames of the stretch before a
// second of the video.
type damagedVideo struct {
	*framedVideo
	before  float64
	failure error
}

func (v *damagedVideo) Frames(ctx context.Context, file *os.File, request ffmpegworker.FramesRequest, each func([]byte) error) (ffmpegworker.FramesResult, error) {
	if len(request.Times) > 0 && request.Times[0] < v.before {
		return ffmpegworker.FramesResult{}, v.failure
	}
	return v.framedVideo.Frames(ctx, file, request, each)
}

// A card whose key frames cannot be taken is that card's failure, after
// three tries when FFmpeg may do better the next time: the video's other
// cards are described, with tries of their own.
func TestACardWhoseFramesCannotBeTakenLeavesTheOthers(t *testing.T) {
	for name, c := range map[string]struct {
		failure error
		class   string
		tries   int
	}{
		"an FFmpeg that fails is tried three times": {ffmpegworker.ErrFailed, "io", 3},
		"frames that do not decode fail at once":    {ffmpegworker.ErrUnreadable, "undecodable", 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDescribeFixture(t)
			// The first card's key frames lie before 20 seconds.
			f.media = &damagedVideo{framedVideo: f.montageFixture.video, before: 20, failure: c.failure}
			f.restart(t)
			for try := 1; try < c.tries; try++ {
				if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 1 {
					t.Fatalf("try %d took %d rounds", try, rounds)
				}
				f.wantJob(t, describeJob, jobRow{state: "pending", class: c.class, attempts: try})
				if stored := f.descriptions(t); len(stored) != 0 {
					t.Fatalf("stored after try %d = %+v", try, stored)
				}
				f.clock.Advance(time.Hour)
			}
			// The last try stores the failure, and the next card follows.
			if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || f.embedder.calls() != 1 {
				t.Fatalf("the last try: %d rounds, %d descriptions", rounds, f.embedder.calls())
			}
			stored := f.descriptions(t)
			if len(stored) != 2 || stored[0].card != 0 || stored[0].result != "" || stored[0].failure != c.class ||
				stored[1].card != 2 || stored[1].failure != "" || stored[1].result == "" {
				t.Fatalf("stored = %+v", stored)
			}
			if state := f.lookup(t).Descriptions; state != photos.AnalysisDone || f.montageJobs(t) != 0 {
				t.Fatalf("descriptions = %q, %d montage jobs left", state, f.montageJobs(t))
			}
			analysis, err := f.analysis(t)
			if err != nil || len(analysis.Descriptions) != 2 || analysis.Descriptions[0].Error != c.class || analysis.Descriptions[1].Description == nil {
				t.Fatalf("AnalysisOfGrant() = %+v, %v", analysis, err)
			}
		})
	}
}

// An original that cannot be opened for a fault of storage is tried again
// with an attempt spent, as for the cards' quality; one that is gone fails
// the descriptions until a retry.
func TestDescriptionsOfAnOriginalThatCannotBeOpenedAreTriedAgain(t *testing.T) {
	ctx := context.Background()
	f := newDescribeFixture(t)
	original := filepath.Join(f.root, "objects", f.object[0:2], f.object[2:4], f.object)
	content, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	// A link to itself opens with an error that is not "does not exist".
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(original), original); err != nil {
		t.Fatal(err)
	}
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 1 {
		t.Fatalf("rounds with the original unreadable = %d", rounds)
	}
	f.wantJob(t, describeJob, jobRow{state: "pending", class: "io", attempts: 1})
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	processAI(t, f.service, f.embedder, f.gate)
	f.wantJob(t, describeJob, jobRow{state: "failed", class: "missing_original", attempts: 2})
	if state := f.lookup(t).Descriptions; state != photos.AnalysisFailed || f.embedder.calls() != 0 {
		t.Fatalf("descriptions with the original gone = %q after %d descriptions", state, f.embedder.calls())
	}
	if err := os.WriteFile(original, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.AnalyzeGrant(ctx, granting(namedAlice), f.grant.ID, []string{f.video.ID}, true); err != nil {
		t.Fatal(err)
	}
	processAI(t, f.service, f.embedder, f.gate)
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone || len(f.descriptions(t)) != 2 {
		t.Fatalf("descriptions with the original back = %q, %d stored", state, len(f.descriptions(t)))
	}
}

// The quality queued for a video goes when its last grant does (#556), and
// another grant has it finished: the descriptions are queued then, and each
// card is described once however often grants go and come.
func TestDescriptionsAreMadeOnceWhenAGrantWentAndAnotherCame(t *testing.T) {
	ctx := context.Background()
	f := describeFixture{montageFixture: newMontageFixture(t), embedder: newDescribingEmbedder()}
	f.enableCompute(t)
	private, _ := libraries(t, f.service, namedAlice)
	f.video = importPhoto(t, f.service, namedAlice, private.ID, "", "trip.mp4", mp4Bytes(1))
	f.grant, f.object = f.montageFixture.grant(t, namedAlice, "project:first", f.video)
	// The grant goes with one of the two cards measured.
	for round := 1; f.count(t, "SELECT COUNT(*) FROM video_card_quality") == 0; round++ {
		if worked, err := f.service.ProcessAIJob(ctx, f.embedder, f.gate); !worked || err != nil || round > 10 {
			t.Fatalf("round %d before the first card's quality = %v, %v", round, worked, err)
		}
	}
	if err := f.service.RevokeGrant(ctx, f.grant.ID); err != nil {
		t.Fatal(err)
	}
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.montageJobs(t) != 0 || f.embedder.calls() != 0 {
		t.Fatalf("with no grant: %d rounds, %d montage jobs, %d descriptions", rounds, f.montageJobs(t), f.embedder.calls())
	}
	f.grant, _ = f.montageFixture.grant(t, namedAlice, "project:second", f.video)
	processAI(t, f.service, f.embedder, f.gate)
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone || f.embedder.calls() != 2 || len(f.descriptions(t)) != 2 || f.montageJobs(t) != 0 {
		t.Fatalf("with the second grant: descriptions %q, %d calls, %d stored, %d montage jobs", state, f.embedder.calls(), len(f.descriptions(t)), f.montageJobs(t))
	}
	// A third finds everything done.
	if err := f.service.RevokeGrant(ctx, f.grant.ID); err != nil {
		t.Fatal(err)
	}
	f.grant, _ = f.montageFixture.grant(t, namedAlice, "project:third", f.video)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 2 {
		t.Fatalf("with the third grant: %d rounds, %d descriptions in all", rounds, f.embedder.calls())
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("descriptions with the third grant = %q", state)
	}
}

// describedUnder describes two cards under release, the second failing as
// a card with too many key frames for the reader's context did (#583).
func describedUnder(t *testing.T, f *describeFixture, release string) []storedDescription {
	t.Helper()
	f.release = release
	f.restart(t)
	processAI(t, f.service, f.embedder, f.gate)
	f.embedder.answer = func(call int, _ photos.CardLayout) (json.RawMessage, error) {
		if call == 2 {
			return nil, fmt.Errorf("%w: llama-server answered 400: the request exceeds the available context size", photos.ErrAIRejected)
		}
		return json.RawMessage(`{"camera":"地面","summary":"湖边"}`), nil
	}
	f.cards(t, []standInCard{
		{start: 0, end: 10000, keyframes: []float64{1, 5, 9}, reasons: "[]"},
		{start: 10000, end: 20000, keyframes: []float64{11, 15, 19}, reasons: "[]"},
	})
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 2 || f.embedder.calls() != 2 {
		t.Fatalf("%d rounds, %d descriptions", rounds, f.embedder.calls())
	}
	described := f.descriptions(t)
	if len(described) != 2 || described[0].result == "" || described[1].result != "" || described[1].failure == "" ||
		described[1].release != release {
		t.Fatalf("descriptions = %+v", described)
	}
	f.embedder.answer = func(int, photos.CardLayout) (json.RawMessage, error) {
		return json.RawMessage(`{"camera":"地面","summary":"山顶"}`), nil
	}
	return described
}

// Each release tries once more the cards whose description failed under
// another, for how it fits key frames or asks about them may now get them
// through (#583). The cards described stay as they are; another start of
// the same release asks about nothing.
func TestAnUpdateTriesTheFailedDescriptionsOnce(t *testing.T) {
	f := newDescribeFixture(t)
	before := describedUnder(t, &f, "1.0.0")

	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 2 {
		t.Fatalf("the same release again: %d rounds, %d descriptions in all", rounds, f.embedder.calls())
	}

	f.release = "1.0.1"
	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 1 || f.embedder.calls() != 3 {
		t.Fatalf("the update: %d rounds, %d descriptions in all, want only the failed card again", rounds, f.embedder.calls())
	}
	stored := f.descriptions(t)
	if len(stored) != 2 || stored[0] != before[0] || !strings.Contains(stored[1].result, "山顶") || stored[1].failure != "" ||
		stored[1].release != "1.0.1" {
		t.Fatalf("stored = %+v, want card 0 as before %+v", stored, before[0])
	}
	analysis, err := f.analysis(t)
	if err != nil || analysis.Described != photos.AnalysisDone || analysis.DescriptionsIdentity != before[0].identity ||
		len(analysis.Descriptions) != 2 || analysis.Descriptions[1].Description == nil {
		t.Fatalf("AnalysisOfGrant() = %+v, %v", analysis, err)
	}

	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 3 {
		t.Fatalf("the update again: %d rounds, %d descriptions in all", rounds, f.embedder.calls())
	}
}

// A video no grant refers to when a release starts keeps its failed card;
// a grant that asks for it again tries the card under that release, as it
// would a stale one, and the lookup does not call the video described
// before then.
func TestARegrantTriesTheFailedDescriptionsOfAnotherRelease(t *testing.T) {
	f := newDescribeFixture(t)
	before := describedUnder(t, &f, "1.0.0")
	if err := f.service.RevokeGrant(t.Context(), f.grant.ID); err != nil {
		t.Fatal(err)
	}

	f.release = "1.0.1"
	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 2 || f.montageJobs(t) != 0 {
		t.Fatalf("revoked: %d rounds, %d descriptions in all, %d montage jobs", rounds, f.embedder.calls(), f.montageJobs(t))
	}
	if stored := f.descriptions(t); len(stored) != 2 || stored[1] != before[1] {
		t.Fatalf("revoked: stored = %+v", stored)
	}

	f.grant, _ = f.montageFixture.grant(t, namedAlice, "project:again", f.video)
	if state := f.lookup(t).Descriptions; state == photos.AnalysisDone {
		t.Fatalf("descriptions with a card failed under the last release = %q", state)
	}
	processAI(t, f.service, f.embedder, f.gate)
	if f.embedder.calls() != 3 {
		t.Fatalf("%d descriptions in all, want only the failed card again", f.embedder.calls())
	}
	if stored := f.descriptions(t); len(stored) != 2 || stored[0] != before[0] || !strings.Contains(stored[1].result, "山顶") {
		t.Fatalf("stored = %+v", stored)
	}
	if state := f.lookup(t).Descriptions; state != photos.AnalysisDone {
		t.Fatalf("descriptions after the retry = %q", state)
	}
}

// The failures written before descriptions recorded their release are
// tried once by the first release that does; without a release, as in
// development and Fake mode, no failure is tried again.
func TestFailuresWithoutARecordedReleaseAreTriedOnce(t *testing.T) {
	f := newDescribeFixture(t)
	before := describedUnder(t, &f, "")
	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 0 || f.embedder.calls() != 2 {
		t.Fatalf("without a release: %d rounds, %d descriptions in all", rounds, f.embedder.calls())
	}
	if _, err := catalog(t, f.root).Exec("UPDATE video_card_descriptions SET written_release = NULL"); err != nil {
		t.Fatal(err)
	}

	f.release = "1.0.0"
	f.restart(t)
	if rounds := processAI(t, f.service, f.embedder, f.gate); rounds != 1 || f.embedder.calls() != 3 {
		t.Fatalf("the first release with the column: %d rounds, %d descriptions in all", rounds, f.embedder.calls())
	}
	stored := f.descriptions(t)
	if len(stored) != 2 || stored[0].result != before[0].result || stored[0].release != "" ||
		!strings.Contains(stored[1].result, "山顶") || stored[1].release != "1.0.0" {
		t.Fatalf("stored = %+v", stored)
	}
}
