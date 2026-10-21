package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/files"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

var (
	alice = accounts.User{ID: "user:alice", Username: "alice", Role: accounts.RoleMember, Status: accounts.UserStatusActive}
	bob   = accounts.User{ID: "user:bob", Username: "bob", Role: accounts.RoleAdmin, Status: accounts.UserStatusActive}
)

// testFiles serves files from a directory as entries of one space.
type testFiles struct {
	dir     string
	entries map[string]files.Entry
	owners  map[string]string
}

func (f *testFiles) add(t *testing.T, owner, id, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, id), bytes.Repeat([]byte{1}, size), 0o600); err != nil {
		t.Fatal(err)
	}
	f.entries[id] = files.Entry{ID: id, SpaceID: "space:" + owner, Name: name, Kind: files.EntryKindFile, SizeBytes: int64(size)}
	f.owners[id] = owner
}

func (f *testFiles) Lookup(_ context.Context, actor accounts.User, id string) (files.Entry, string, error) {
	entry, ok := f.entries[id]
	if !ok || f.owners[id] != actor.ID {
		return files.Entry{}, "", files.ErrNotFound
	}
	return entry, entry.Name, nil
}

func (f *testFiles) OpenContent(ctx context.Context, actor accounts.User, id string) (files.Content, error) {
	entry, _, err := f.Lookup(ctx, actor, id)
	if err != nil {
		return files.Content{}, err
	}
	file, err := os.Open(filepath.Join(f.dir, id))
	if err != nil {
		return files.Content{}, err
	}
	return files.Content{Name: entry.Name, SizeBytes: entry.SizeBytes, Reader: file}, nil
}

// testMedia decodes every file to Duration of speech: Opus is the file's
// name and the piece, PCM is silence.
type testMedia struct {
	duration time.Duration
	codec    string
	mu       sync.Mutex
	speech   []mediaworker.SpeechRequest
}

func (m *testMedia) Probe(context.Context, *os.File) (mediaworker.Probe, error) {
	if m.codec == "" {
		return mediaworker.Probe{Format: mediaworker.ProbeFormat{Name: "mp4", Duration: 3}}, nil
	}
	return mediaworker.Probe{
		Format:  mediaworker.ProbeFormat{Name: "mov,mp4,m4a,3gp,3g2,mj2", Duration: m.duration.Seconds()},
		Streams: []mediaworker.ProbeStream{{Type: "audio", Codec: m.codec}},
	}, nil
}

func (m *testMedia) Audio(context.Context, *os.File, mediaworker.AudioRequest) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("fLaC")), nil
}

func (m *testMedia) Speech(_ context.Context, file *os.File, request mediaworker.SpeechRequest) (io.ReadCloser, error) {
	m.mu.Lock()
	m.speech = append(m.speech, request)
	m.mu.Unlock()
	if request.Format == mediaworker.SpeechOpus {
		return io.NopCloser(strings.NewReader(fmt.Sprintf("%s@%.0f", filepath.Base(file.Name()), request.Start))), nil
	}
	length := request.Duration
	if length == 0 {
		length = m.duration.Seconds() - request.Start
	}
	samples := make([]byte, 2*int(length*16000))
	for i := 0; i < len(samples); i += 2 {
		binary.LittleEndian.PutUint16(samples[i:], uint16(i%200))
	}
	return io.NopCloser(bytes.NewReader(samples)), nil
}

// scriptedRecognizer answers each piece with sentences every 5 seconds, by
// voice label in turn.
type scriptedRecognizer struct {
	ready  bool
	labels []string
	err    error
	mu     sync.Mutex
	calls  []RecognizeRequest
}

func (r *scriptedRecognizer) Ready(context.Context) bool { return r.ready }

func (r *scriptedRecognizer) Recognize(_ context.Context, _ []byte, request RecognizeRequest) (Recognition, error) {
	r.mu.Lock()
	r.calls = append(r.calls, request)
	r.mu.Unlock()
	if r.err != nil {
		return Recognition{}, r.err
	}
	var recognition Recognition
	for at, i := int64(0), 0; at+4000 <= request.Duration.Milliseconds(); at, i = at+5000, i+1 {
		label := ""
		if len(r.labels) > 0 {
			label = r.labels[i%len(r.labels)]
		}
		recognition.Utterances = append(recognition.Utterances, Recognized{StartMs: at, EndMs: at + 4000, Text: fmt.Sprintf("第 %d 句", i+1), Speaker: label})
	}
	return recognition, nil
}

type fixture struct {
	service    *Service
	files      *testFiles
	media      *testMedia
	recognizer *scriptedRecognizer
	sessions   []accounts.Session
	mu         sync.Mutex
}

func newFixture(t *testing.T, options Options) *fixture {
	t.Helper()
	store, err := OpenSQLite(filepath.Join(t.TempDir(), "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{
		files:      &testFiles{dir: t.TempDir(), entries: map[string]files.Entry{}, owners: map[string]string{}},
		media:      &testMedia{duration: 30 * time.Second, codec: "aac"},
		recognizer: &scriptedRecognizer{ready: true, labels: []string{"1", "2"}},
		sessions:   []accounts.Session{{Token: "token-alice", User: alice}, {Token: "token-bob", User: bob}},
	}
	options.Media = f.media
	if options.Recognizer == nil {
		options.Recognizer = f.recognizer
	}
	if options.TextModel == nil {
		options.TextModel = FakeTextModel{}
	}
	if options.Voiceprints == nil {
		options.Voiceprints = FakeVoiceprints{}
	}
	options.Sessions = func(context.Context) []accounts.Session {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]accounts.Session(nil), f.sessions...)
	}
	options.Poll = 20 * time.Millisecond
	f.service = New(store, f.files, options)
	f.service.Run()
	t.Cleanup(func() {
		f.service.Close()
		_ = store.Close()
	})
	return f
}

// settle waits until no recording or job is queued or running.
func (f *fixture) settle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var busy int
		err := f.service.store.db.QueryRow(`SELECT
    (SELECT COUNT(*) FROM recordings WHERE status IN ('queued', 'processing')) +
    (SELECT COUNT(*) FROM translations WHERE status IN ('queued', 'running')) +
    (SELECT COUNT(*) FROM summaries WHERE status IN ('queued', 'running'))`).Scan(&busy)
		if err != nil {
			t.Fatal(err)
		}
		if busy == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("processing did not settle")
}

func (f *fixture) imported(t *testing.T, owner accounts.User, id string, request ImportRequest) Detail {
	t.Helper()
	f.files.add(t, owner.ID, id, id+".m4a", 64)
	request.FileID = id
	recording, err := f.service.Import(context.Background(), owner, request)
	if err != nil {
		t.Fatalf("Import(%s) error = %v", id, err)
	}
	f.settle(t)
	detail, err := f.service.Get(context.Background(), owner, recording.ID)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}

func TestImportTranscribesNamesVoicesAndRunsTheRequestedJobs(t *testing.T) {
	f := newFixture(t, Options{})
	detail := f.imported(t, alice, "file:meeting", ImportRequest{Template: "meeting", TranslateTo: "en", Hotwords: []string{" 橱柜 ", "橱柜"}})
	if detail.Status != StatusReady || detail.DurationMs != 30000 || len(detail.Utterances) != 6 {
		t.Fatalf("detail = %+v", detail.Recording)
	}
	if len(detail.SpeakerList) != 2 || detail.SpeakerList[0].Label != "发言人 1" || detail.SpeakerList[1].Number != 2 || !detail.SpeakerList[0].Voiceprint {
		t.Fatalf("speakers = %+v", detail.SpeakerList)
	}
	if detail.Utterances[0].Speaker != "s1" || detail.Utterances[1].Speaker != "s2" || detail.SpeakerList[0].SpeechMs != 12000 {
		t.Fatalf("utterances = %+v", detail.Utterances)
	}
	if len(f.recognizer.calls) != 1 || len(f.recognizer.calls[0].Hotwords) != 1 || f.recognizer.calls[0].Hotwords[0] != "橱柜" {
		t.Fatalf("recognition requests = %+v", f.recognizer.calls)
	}
	if len(detail.Summaries) != 1 || detail.Summaries[0].Status != JobReady || detail.Summaries[0].Content == nil ||
		len(detail.Summaries[0].Content.Sections) != 3 || detail.Summaries[0].Content.Sections[1].Title != "决定事项" {
		t.Fatalf("summaries = %+v", detail.Summaries)
	}
	if detail.Title != "家庭装修安排讨论" {
		t.Fatalf("title = %q, want the summary's", detail.Title)
	}
	if len(detail.Translations) != 1 || detail.Translations[0].Status != JobReady || len(detail.Translations[0].Lines) != 6 ||
		!strings.HasPrefix(detail.Translations[0].Lines[0].Text, "〔英语〕") || detail.Translations[0].Revision != detail.TextRevision {
		t.Fatalf("translations = %+v", detail.Translations)
	}
	if _, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:meeting"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Import() = %v, want ErrDuplicate", err)
	}
	list, err := f.service.List(context.Background(), alice)
	if err != nil || len(list) != 1 || list[0].Speakers != 2 || !strings.Contains(list[0].Preview, "本地演示摘要") {
		t.Fatalf("List() = %+v, %v", list, err)
	}
}

func TestMembersSeeOnlyTheirOwnRecordingsAndPeople(t *testing.T) {
	f := newFixture(t, Options{})
	detail := f.imported(t, alice, "file:a", ImportRequest{})
	if _, err := f.service.Get(context.Background(), bob, detail.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob Get() = %v", err)
	}
	if list, err := f.service.List(context.Background(), bob); err != nil || len(list) != 0 {
		t.Fatalf("bob List() = %+v, %v", list, err)
	}
	if _, err := f.service.AssignSpeaker(context.Background(), bob, detail.ID, "s1", AssignRequest{Name: "张三"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob AssignSpeaker() = %v", err)
	}
	if _, err := f.service.Import(context.Background(), bob, ImportRequest{FileID: "file:a"}); !errors.Is(err, files.ErrNotFound) {
		t.Fatalf("bob Import(alice's file) = %v", err)
	}
	if _, err := f.service.AssignSpeaker(context.Background(), alice, detail.ID, "s1", AssignRequest{Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	if people, err := f.service.People(context.Background(), bob); err != nil || len(people) != 0 {
		t.Fatalf("bob People() = %+v, %v", people, err)
	}
}

func TestNamingAVoiceRecognisesItInOtherRecordings(t *testing.T) {
	f := newFixture(t, Options{})
	first := f.imported(t, alice, "file:first", ImportRequest{})
	second := f.imported(t, alice, "file:second", ImportRequest{})
	named, err := f.service.AssignSpeaker(context.Background(), alice, first.ID, "s1", AssignRequest{Name: " 张三 "})
	if err != nil || named.SpeakerList[0].Label != "张三" || named.SpeakerList[0].Assigned != AssignedUser {
		t.Fatalf("AssignSpeaker() = %+v, %v", named.SpeakerList, err)
	}
	other, err := f.service.Get(context.Background(), alice, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if other.SpeakerList[0].Label != "张三" || other.SpeakerList[0].Assigned != AssignedAuto || other.SpeakerList[0].Score < AutoThreshold {
		t.Fatalf("the same voice elsewhere = %+v", other.SpeakerList[0])
	}
	if other.SpeakerList[1].PersonID != "" || other.NamesRevision <= second.NamesRevision {
		t.Fatalf("other speaker = %+v, names revision %d → %d", other.SpeakerList[1], second.NamesRevision, other.NamesRevision)
	}
	// A recording imported after the naming is recognised as it is processed.
	third := f.imported(t, alice, "file:third", ImportRequest{})
	if third.SpeakerList[0].Label != "张三" || third.People[0] != "张三" {
		t.Fatalf("a new recording = %+v", third.SpeakerList)
	}
	if calls := f.recognizer.calls; len(calls[len(calls)-1].Hotwords) != 1 || calls[len(calls)-1].Hotwords[0] != "张三" {
		t.Fatalf("people's names are hotwords: %+v", calls[len(calls)-1])
	}
	// 不是他: the name goes and never comes back for that speaker.
	rejected, err := f.service.RejectPerson(context.Background(), alice, other.ID, "s1", other.SpeakerList[0].PersonID)
	if err != nil || rejected.SpeakerList[0].PersonID != "" || rejected.SpeakerList[0].Suggest != nil {
		t.Fatalf("RejectPerson() = %+v, %v", rejected.SpeakerList, err)
	}
	people, err := f.service.People(context.Background(), alice)
	if err != nil || len(people) != 1 || people[0].Samples != 1 || people[0].Recordings != 2 {
		t.Fatalf("People() = %+v, %v", people, err)
	}
	// Deleting the person names nobody after them any more.
	if err := f.service.DeletePerson(context.Background(), alice, people[0].ID); err != nil {
		t.Fatal(err)
	}
	after, _ := f.service.Get(context.Background(), alice, third.ID)
	if after.SpeakerList[0].PersonID != "" || after.SpeakerList[0].Label != "发言人 1" {
		t.Fatalf("after deleting the person = %+v", after.SpeakerList[0])
	}
}

func TestPeopleRenameMergeAndNameClashes(t *testing.T) {
	f := newFixture(t, Options{})
	detail := f.imported(t, alice, "file:a", ImportRequest{})
	if _, err := f.service.AssignSpeaker(context.Background(), alice, detail.ID, "s1", AssignRequest{Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AssignSpeaker(context.Background(), alice, detail.ID, "s2", AssignRequest{Name: "小张"}); err != nil {
		t.Fatal(err)
	}
	people, _ := f.service.People(context.Background(), alice)
	if len(people) != 2 {
		t.Fatalf("people = %+v", people)
	}
	byName := map[string]string{people[0].Name: people[0].ID, people[1].Name: people[1].ID}
	if _, err := f.service.RenamePerson(context.Background(), alice, byName["小张"], "张三"); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("RenamePerson(clash) = %v", err)
	}
	merged, err := f.service.MergePeople(context.Background(), alice, byName["小张"], byName["张三"])
	if err != nil || merged.Samples != 2 || merged.Recordings != 1 {
		t.Fatalf("MergePeople() = %+v, %v", merged, err)
	}
	renamed, err := f.service.RenamePerson(context.Background(), alice, merged.ID, "张伟")
	if err != nil || renamed.Name != "张伟" {
		t.Fatalf("RenamePerson() = %+v, %v", renamed, err)
	}
	after, _ := f.service.Get(context.Background(), alice, detail.ID)
	if after.SpeakerList[0].Label != "张伟" || after.SpeakerList[1].Label != "张伟" {
		t.Fatalf("speakers = %+v", after.SpeakerList)
	}
	if _, err := f.service.AssignSpeaker(context.Background(), alice, detail.ID, "s1", AssignRequest{Name: strings.Repeat("长", 41)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a long name = %v", err)
	}
}

func TestEditingAndMergingMakeResultsOutOfDate(t *testing.T) {
	f := newFixture(t, Options{})
	detail := f.imported(t, alice, "file:a", ImportRequest{Template: "general", TranslateTo: "ja"})
	text := "改过的第一句"
	edited, err := f.service.EditUtterance(context.Background(), alice, detail.ID, 1, &text, nil)
	if err != nil || edited.Utterances[0].Text != text || !edited.Utterances[0].Edited || edited.TextRevision != detail.TextRevision+1 {
		t.Fatalf("EditUtterance() = %+v, %v", edited.Utterances[0], err)
	}
	if edited.Translations[0].Revision == edited.TextRevision {
		t.Fatal("the translation should be out of date")
	}
	merged, err := f.service.MergeSpeakers(context.Background(), alice, detail.ID, "s2", "s1")
	if err != nil || len(merged.SpeakerList) != 1 || merged.SpeakerList[0].SpeechMs != 24000 || merged.NamesRevision <= edited.NamesRevision {
		t.Fatalf("MergeSpeakers() = %+v, %v", merged.SpeakerList, err)
	}
	for _, utterance := range merged.Utterances {
		if utterance.Speaker != "s1" {
			t.Fatalf("utterance %d still has %s", utterance.Seq, utterance.Speaker)
		}
	}
	again, err := f.service.RequestSummary(context.Background(), alice, detail.ID, "general")
	if err != nil || again.Summaries[0].Status != JobQueued && again.Summaries[0].Status != JobRunning {
		t.Fatalf("RequestSummary() = %+v, %v", again.Summaries, err)
	}
	f.settle(t)
	done, _ := f.service.Get(context.Background(), alice, detail.ID)
	if done.Summaries[0].TextRevision != done.TextRevision || done.Summaries[0].NamesRevision != done.NamesRevision {
		t.Fatalf("summary revisions = %+v, recording %d/%d", done.Summaries[0], done.TextRevision, done.NamesRevision)
	}
	empty := ""
	if _, err := f.service.EditUtterance(context.Background(), alice, detail.ID, 1, &empty, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty sentence = %v", err)
	}
	if err := f.service.DeleteTranslation(context.Background(), alice, detail.ID, "ja"); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DeleteTranslation(context.Background(), alice, detail.ID, "ja"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting it again = %v", err)
	}
}

func TestRecordingsWaitForTheirMemberAndTheRecognizer(t *testing.T) {
	f := newFixture(t, Options{})
	f.mu.Lock()
	f.sessions = nil
	f.mu.Unlock()
	f.files.add(t, alice.ID, "file:a", "a.m4a", 8)
	recording, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:a"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor := func(status, waiting string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			current, _ := f.service.owned(context.Background(), alice, recording.ID)
			if current.Status == status && current.Waiting == waiting {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		current, _ := f.service.owned(context.Background(), alice, recording.ID)
		t.Fatalf("recording = %s/%s, want %s/%s", current.Status, current.Waiting, status, waiting)
	}
	waitFor(StatusWaiting, WaitSignIn)
	f.mu.Lock()
	f.sessions = []accounts.Session{{Token: "token", User: alice}}
	f.mu.Unlock()
	f.recognizer.mu.Lock()
	f.recognizer.ready = false
	f.recognizer.mu.Unlock()
	waitFor(StatusWaiting, WaitRecognizer)
	f.recognizer.mu.Lock()
	f.recognizer.ready = true
	f.recognizer.mu.Unlock()
	f.service.Changed()
	waitFor(StatusReady, "")
}

func TestRejectedCredentialsFailAndRetryStartsAgain(t *testing.T) {
	f := newFixture(t, Options{})
	f.recognizer.err = fmt.Errorf("%w: Invalid X-Api-Key", ErrRecognizerRejected)
	detail := f.imported(t, alice, "file:a", ImportRequest{})
	if detail.Status != StatusFailed || detail.Error == nil || detail.Error.Code != FailRejected {
		t.Fatalf("detail = %+v", detail.Recording)
	}
	f.recognizer.err = nil
	if _, err := f.service.Retry(context.Background(), alice, detail.ID); err != nil {
		t.Fatal(err)
	}
	f.settle(t)
	again, _ := f.service.Get(context.Background(), alice, detail.ID)
	if again.Status != StatusReady || again.Error != nil {
		t.Fatalf("after retry = %+v", again.Recording)
	}
	if _, err := f.service.Retry(context.Background(), alice, detail.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retrying a ready recording = %v", err)
	}
}

func TestLongRecordingsAreRecognisedInPiecesAndTheirVoicesJoined(t *testing.T) {
	f := newFixture(t, Options{Chunk: 20 * time.Second})
	f.media.duration = 50 * time.Second
	detail := f.imported(t, alice, "file:long", ImportRequest{})
	if len(f.recognizer.calls) != 3 || f.recognizer.calls[2].Duration != 10*time.Second {
		t.Fatalf("recognition requests = %+v", f.recognizer.calls)
	}
	// Each piece labels its voices 1 and 2; their profiles agree, so the
	// recording has two speakers, not six.
	if len(detail.SpeakerList) != 2 || detail.Utterances[4].StartMs != 20000 || detail.Utterances[4].Speaker != "s1" {
		t.Fatalf("speakers = %+v, utterances = %+v", detail.SpeakerList, detail.Utterances)
	}
}

func TestUnlabeledSpeechIsClustered(t *testing.T) {
	f := newFixture(t, Options{})
	f.recognizer.labels = nil
	detail := f.imported(t, alice, "file:a", ImportRequest{})
	if len(detail.SpeakerList) != 1 || !detail.SpeakerList[0].Voiceprint || detail.Utterances[5].Speaker != "s1" {
		t.Fatalf("speakers = %+v", detail.SpeakerList)
	}
}

func TestWithoutVoiceprintsSpeakersStayAsLabelled(t *testing.T) {
	f := newFixture(t, Options{Voiceprints: brokenVoiceprints{}})
	detail := f.imported(t, alice, "file:a", ImportRequest{})
	if detail.Status != StatusReady || len(detail.SpeakerList) != 2 || detail.SpeakerList[0].Voiceprint || detail.Voiceprints {
		t.Fatalf("detail = %+v %+v", detail.Recording, detail.SpeakerList)
	}
}

type brokenVoiceprints struct{}

func (brokenVoiceprints) Embed(context.Context, []int16, []Group) (string, [][]float32, error) {
	return "", nil, errors.New("voice worker is unavailable")
}

func (brokenVoiceprints) Cluster(context.Context, []int16, []Span, float64) (string, []int, [][]float32, error) {
	return "", nil, nil, errors.New("voice worker is unavailable")
}

func TestImportRefusesWhatCannotBeProcessed(t *testing.T) {
	f := newFixture(t, Options{})
	f.files.add(t, alice.ID, "file:a", "a.m4a", 8)
	for _, request := range []ImportRequest{{FileID: "file:a", Language: "xx"}, {FileID: "file:a", Template: "poem"}, {FileID: "file:a", TranslateTo: "tlh"}} {
		if _, err := f.service.Import(context.Background(), alice, request); !errors.Is(err, ErrInvalid) {
			t.Errorf("Import(%+v) = %v", request, err)
		}
	}
	f.media.codec = ""
	if _, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:a"}); !errors.Is(err, ErrNotAudio) {
		t.Fatalf("a file without audio = %v", err)
	}
	f.media.codec, f.media.duration = "aac", 6*time.Hour
	if _, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:a"}); !errors.Is(err, ErrTooLong) {
		t.Fatalf("a six-hour file = %v", err)
	}
	f.media.codec, f.media.duration = "amr_nb", time.Minute
	recording, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:a"})
	if err != nil || !recording.Transcode {
		t.Fatalf("an AMR recording = %+v, %v", recording, err)
	}
}

func TestDeletingARecordingForgetsWhatItTaught(t *testing.T) {
	f := newFixture(t, Options{})
	first := f.imported(t, alice, "file:first", ImportRequest{})
	second := f.imported(t, alice, "file:second", ImportRequest{})
	if _, err := f.service.AssignSpeaker(context.Background(), alice, first.ID, "s1", AssignRequest{Name: "张三"}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Delete(context.Background(), alice, first.ID); err != nil {
		t.Fatal(err)
	}
	other, _ := f.service.Get(context.Background(), alice, second.ID)
	if other.SpeakerList[0].PersonID != "" {
		t.Fatalf("still recognised after the only sample went: %+v", other.SpeakerList[0])
	}
	if _, err := f.service.Get(context.Background(), alice, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(deleted) = %v", err)
	}
}

func TestRecognizerSettingsAreAdministratorsAndNeverShowTheSecret(t *testing.T) {
	f := newFixture(t, Options{Recognizer: Doubao{}})
	f.service.recognizer = Doubao{Credentials: f.service.store.DoubaoCredentials}
	if _, err := f.service.RecognizerSettings(context.Background(), alice); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member reads settings: %v", err)
	}
	if _, err := f.service.SetRecognizer(context.Background(), bob, RecognizerCredentials{Mode: ModeAppKey, AppID: "123"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("APP ID without token = %v", err)
	}
	settings, err := f.service.SetRecognizer(context.Background(), bob, RecognizerCredentials{Mode: ModeAppKey, AppID: " 1234567 ", AccessToken: "secret-token-abcd"})
	if err != nil || !settings.Configured || settings.AppID != "1234567" || settings.SecretHint != "••••abcd" || settings.UpdatedBy != "bob" {
		t.Fatalf("SetRecognizer() = %+v, %v", settings, err)
	}
	encoded, _ := json.Marshal(settings)
	if strings.Contains(string(encoded), "secret-token") {
		t.Fatalf("settings show the secret: %s", encoded)
	}
	if !f.service.Status(context.Background()).Recognizer.Available {
		t.Fatal("status should say the recognizer is configured")
	}
	if err := f.service.ClearRecognizer(context.Background(), bob); err != nil {
		t.Fatal(err)
	}
	if settings, _ := f.service.RecognizerSettings(context.Background(), bob); settings.Configured {
		t.Fatalf("after clearing = %+v", settings)
	}
}

func TestDoubaoSpeaksTheFlashProtocol(t *testing.T) {
	var got struct {
		header http.Header
		body   doubaoRequest
	}
	status := "20000000"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.header = r.Header.Clone()
		got.body = doubaoRequest{}
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("X-Api-Status-Code", status)
		w.Header().Set("X-Api-Message", "OK")
		w.Header().Set("X-Tt-Logid", "log-1")
		switch status {
		case "45000010":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":45000010,"message":"Invalid X-Api-Key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"audio_info":{"duration":5200},"result":{"text":"你好。我在。","utterances":[
{"start_time":100,"end_time":900,"text":"你好。","additions":{"speaker":"1"}},
{"start_time":1200,"end_time":2600,"text":"我在。","additions":{"speaker":2}},
{"start_time":3000,"end_time":3100,"text":"  "}]}}`))
	}))
	defer server.Close()
	credentials := DoubaoCredentials{APIKey: "new-console-key"}
	doubao := Doubao{URL: server.URL, Credentials: func(context.Context) (DoubaoCredentials, error) { return credentials, nil }}
	result, err := doubao.Recognize(context.Background(), []byte("OggS"), RecognizeRequest{Hotwords: []string{"张三", "张三", ""}, Duration: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Utterances) != 2 || result.Utterances[0].Speaker != "1" || result.Utterances[1].Speaker != "2" || result.Utterances[1].EndMs != 2600 {
		t.Fatalf("Recognize() = %+v", result)
	}
	if got.header.Get("X-Api-Key") != "new-console-key" || got.header.Get("X-Api-Resource-Id") != DoubaoResource || got.header.Get("X-Api-Sequence") != "-1" ||
		len(got.header.Get("X-Api-Request-Id")) != 36 || got.header.Get("X-Api-App-Key") != "" {
		t.Fatalf("headers = %v", got.header)
	}
	if got.body.Audio.Format != "ogg" || got.body.Audio.Codec != "opus" || got.body.Audio.Data != "T2dnUw==" || !got.body.Request.EnableSpeakerInfo ||
		got.body.Request.SSDVersion != "200" || !got.body.Request.ShowUtterances || got.body.Request.Corpus == nil ||
		got.body.Request.Corpus.Context != `{"hotwords":[{"word":"张三"}]}` {
		t.Fatalf("body = %+v", got.body)
	}
	credentials = DoubaoCredentials{AppID: "app", AccessToken: "token"}
	if _, err := doubao.Recognize(context.Background(), []byte("OggS"), RecognizeRequest{Language: "en-US"}); err != nil {
		t.Fatal(err)
	}
	if got.header.Get("X-Api-App-Key") != "app" || got.header.Get("X-Api-Access-Key") != "token" || got.body.Audio.Language != "en-US" || got.body.Request.SSDVersion != "" {
		t.Fatalf("old console request = %v %+v", got.header, got.body)
	}
	for code, want := range map[string]error{"45000010": ErrRecognizerRejected, "55000031": ErrRecognizerBusy, "45000151": ErrNotAudio} {
		status = code
		if _, err := doubao.Recognize(context.Background(), []byte("OggS"), RecognizeRequest{}); !errors.Is(err, want) {
			t.Errorf("status %s: error = %v, want %v", code, err, want)
		}
	}
	status = "20000003"
	if result, err := doubao.Recognize(context.Background(), []byte("OggS"), RecognizeRequest{}); err != nil || len(result.Utterances) != 0 {
		t.Fatalf("silence = %+v, %v", result, err)
	}
	credentials = DoubaoCredentials{}
	if _, err := doubao.Recognize(context.Background(), []byte("OggS"), RecognizeRequest{}); !errors.Is(err, ErrRecognizerUnconfigured) {
		t.Fatalf("no credentials = %v", err)
	}
}

func TestChatModelPostsToTheGatewaySocket(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("voice-gateway-%d.sock", time.Now().UnixNano()))
	defer os.Remove(socket)
	var request map[string]any
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"items\":[]}"},"finish_reason":"stop"}]}`))
	}))
	listener, err := netListenUnix(socket)
	if err != nil {
		t.Skipf("cannot listen on a Unix socket: %v", err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	model := NewChatModel(socket, "deepseek-flash")
	answer, err := model.Complete(context.Background(), Prompt{System: "s", User: "u", JSON: true, MaxTokens: 100})
	if err != nil || answer != `{"items":[]}` {
		t.Fatalf("Complete() = %q, %v", answer, err)
	}
	format, _ := request["response_format"].(map[string]any)
	if request["model"] != "deepseek-flash" || format["type"] != "json_object" || request["stream"] != false {
		t.Fatalf("request = %v", request)
	}
}

func TestShapedSummariesKeepTheTemplate(t *testing.T) {
	template, _ := templateByID("call")
	content := shaped(SummaryContent{
		Title: " 物业来电 ", Sections: []SummarySection{{Key: "facts", Items: []string{"押金 2000 元", " "}}, {Key: "made-up", Items: []string{"x"}}},
		Actions: []ActionItem{{Task: " 交押金 ", Owner: "我"}, {Task: ""}}, Keywords: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"},
	}, template)
	if content.Title != "物业来电" || len(content.Sections) != 3 || content.Sections[0].Key != "purpose" || len(content.Sections[0].Items) != 0 ||
		len(content.Sections[1].Items) != 1 || content.Sections[1].Title != "关键信息" || len(content.Actions) != 1 || len(content.Keywords) != 8 {
		t.Fatalf("shaped() = %+v", content)
	}
}

func TestClockAndTitles(t *testing.T) {
	if clock(65_000) != "01:05" || clock(3_725_000) != "1:02:05" {
		t.Fatalf("clock = %s %s", clock(65_000), clock(3_725_000))
	}
	if titleOf("周会 2026-10-20.m4a") != "周会 2026-10-20" || titleOf(".m4a") != ".m4a" {
		t.Fatalf("titleOf = %q %q", titleOf("周会 2026-10-20.m4a"), titleOf(".m4a"))
	}
	if !playable("mov,mp4,m4a,3gp,3g2,mj2", "aac") || playable("amr", "amr_nb") || !playable("wav", "pcm_s16le") {
		t.Fatal("playable() is wrong")
	}
}

func netListenUnix(path string) (net.Listener, error) { return net.Listen("unix", path) }
