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
// voice label in turn; hold keeps its tasks queued.
type scriptedRecognizer struct {
	ready    bool
	labels   []string
	err      error
	hold     bool
	mu       sync.Mutex
	calls    []RecognizeRequest
	tasks    map[string]RecognizeRequest
	released []string
}

func (r *scriptedRecognizer) Ready(context.Context) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

func (r *scriptedRecognizer) Submit(_ context.Context, _ []byte, request RecognizeRequest) (Task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, request)
	if r.err != nil {
		return Task{}, r.err
	}
	if r.tasks == nil {
		r.tasks = map[string]RecognizeRequest{}
	}
	task := Task{ID: fmt.Sprintf("task-%d", len(r.calls)), Object: fmt.Sprintf("a-nas-voice/task-%d.ogg", len(r.calls))}
	r.tasks[task.ID] = request
	return task, nil
}

func (r *scriptedRecognizer) Release(_ context.Context, task Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, task.ID)
	delete(r.tasks, task.ID)
	return nil
}

func (r *scriptedRecognizer) Result(_ context.Context, task Task) (Recognition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.tasks[task.ID]
	switch {
	case !ok:
		return Recognition{}, ErrRecognitionLost
	case r.hold:
		return Recognition{}, ErrRecognizing
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
	store      *Store
	options    Options
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
	options.ResultPoll = 5 * time.Millisecond
	f.store, f.options = store, options
	f.service = New(store, f.files, options)
	f.service.Run()
	t.Cleanup(func() {
		f.service.Close()
		_ = store.Close()
	})
	return f
}

// restart stops the service and starts another on the same database, as a
// restart of the Product Service does.
func (f *fixture) restart() {
	f.service.Close()
	f.service = New(f.store, f.files, f.options)
	f.service.Run()
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
	f.recognizer.mu.Lock()
	f.recognizer.err = fmt.Errorf("%w: Invalid X-Api-Key", ErrRecognizerRejected)
	f.recognizer.mu.Unlock()
	detail := f.imported(t, alice, "file:a", ImportRequest{})
	if detail.Status != StatusFailed || detail.Error == nil || detail.Error.Code != FailRejected {
		t.Fatalf("detail = %+v", detail.Recording)
	}
	f.recognizer.mu.Lock()
	f.recognizer.err = nil
	f.recognizer.mu.Unlock()
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
	storage := StorageInput{Region: "cn-beijing", Bucket: "family-voice", AccessKeyID: "AKLTexample1234", SecretAccessKey: "secret-key-wxyz"}
	if _, err := f.service.SetRecognizer(context.Background(), bob, RecognizerCredentials{Mode: ModeAppKey, AppID: "123", Storage: storage}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("APP ID without token = %v", err)
	}
	for _, wrong := range []StorageInput{
		{Region: "mars-1", Bucket: "family-voice", AccessKeyID: "AK", SecretAccessKey: "SK"},
		{Region: "cn-beijing", Bucket: "Family_Voice", AccessKeyID: "AK", SecretAccessKey: "SK"},
		{Region: "cn-beijing", Bucket: "family-voice", AccessKeyID: "AK"},
	} {
		if _, err := f.service.SetRecognizer(context.Background(), bob, RecognizerCredentials{Mode: ModeAPIKey, APIKey: "key", Storage: wrong}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("storage %+v = %v", wrong, err)
		}
	}
	settings, err := f.service.SetRecognizer(context.Background(), bob, RecognizerCredentials{Mode: ModeAppKey, AppID: " 1234567 ", AccessToken: "secret-token-abcd", Storage: storage})
	if err != nil || !settings.Configured || settings.AppID != "1234567" || settings.SecretHint != "••••abcd" || settings.UpdatedBy != "bob" ||
		settings.Storage == nil || settings.Storage.Bucket != "family-voice" || settings.Storage.AccessKeyHint != "••••1234" || settings.Storage.SecretHint != "••••wxyz" ||
		len(settings.Regions) == 0 {
		t.Fatalf("SetRecognizer() = %+v, %v", settings, err)
	}
	encoded, _ := json.Marshal(settings)
	if strings.Contains(string(encoded), "secret-token") || strings.Contains(string(encoded), "secret-key") || strings.Contains(string(encoded), "AKLTexample") {
		t.Fatalf("settings show a secret: %s", encoded)
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

func TestRecognitionTasksOutliveARestartAndGoWithTheirRecording(t *testing.T) {
	f := newFixture(t, Options{})
	hold := func(on bool) {
		f.recognizer.mu.Lock()
		f.recognizer.hold = on
		f.recognizer.mu.Unlock()
	}
	saved := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		var count int
		for time.Now().Before(deadline) {
			_ = f.store.db.QueryRow("SELECT COUNT(*) FROM recognition_tasks").Scan(&count)
			if count == want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("saved tasks = %d, want %d", count, want)
	}
	submitted := func() int {
		f.recognizer.mu.Lock()
		defer f.recognizer.mu.Unlock()
		return len(f.recognizer.calls)
	}
	hold(true)
	f.files.add(t, alice.ID, "file:a", "a.m4a", 8)
	first, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:a"})
	if err != nil {
		t.Fatal(err)
	}
	saved(1)
	// After a restart the service asks for the same task's result.
	f.restart()
	hold(false)
	f.settle(t)
	if detail, _ := f.service.Get(context.Background(), alice, first.ID); detail.Status != StatusReady || len(detail.Utterances) == 0 {
		t.Fatalf("after the restart = %+v", detail.Recording)
	}
	saved(0)
	if submitted() != 1 || len(f.recognizer.released) != 1 || f.recognizer.released[0] != "task-1" {
		t.Fatalf("submitted %d, released %v", submitted(), f.recognizer.released)
	}
	// A task the service no longer knows is submitted once more.
	hold(true)
	f.files.add(t, alice.ID, "file:b", "b.m4a", 8)
	second, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:b"})
	if err != nil {
		t.Fatal(err)
	}
	saved(1)
	f.restart()
	f.recognizer.mu.Lock()
	f.recognizer.tasks, f.recognizer.hold = nil, false
	f.recognizer.mu.Unlock()
	f.settle(t)
	if detail, _ := f.service.Get(context.Background(), alice, second.ID); detail.Status != StatusReady || submitted() != 3 {
		t.Fatalf("after losing the task = %+v, submitted %d", detail.Recording, submitted())
	}
	// Removing a recording that is still being recognised lets go of its task.
	hold(true)
	f.files.add(t, alice.ID, "file:c", "c.m4a", 8)
	third, err := f.service.Import(context.Background(), alice, ImportRequest{FileID: "file:c"})
	if err != nil {
		t.Fatal(err)
	}
	saved(1)
	if err := f.service.Delete(context.Background(), alice, third.ID); err != nil {
		t.Fatal(err)
	}
	saved(0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.recognizer.mu.Lock()
		released := strings.Join(f.recognizer.released, ",")
		f.recognizer.mu.Unlock()
		if strings.HasSuffix(released, "task-4") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("released = %s, want task-4 last", released)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// memoryRelay keeps relayed audio in memory, by bucket.
type memoryRelay struct {
	mu      sync.Mutex
	objects map[string][]byte
	deleted []string
}

type bucketRelay struct {
	*memoryRelay
	bucket string
}

func (r bucketRelay) Put(_ context.Context, key string, audio []byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.objects[r.bucket+"/"+key] = audio
	return "https://" + r.bucket + ".relay.test/" + key + "?X-Tos-Signature=signed", nil
}

func (r bucketRelay) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, r.bucket+"/"+key)
	delete(r.objects, r.bucket+"/"+key)
	return nil
}

func TestDoubaoSubmitsARelayedRecordingAndAsksForItsResult(t *testing.T) {
	var mu sync.Mutex
	var submitted struct {
		header http.Header
		body   doubaoRequest
	}
	var queried http.Header
	submitStatus, queryStatus := "20000000", "20000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		status := queryStatus
		if r.URL.Path == "/submit" {
			submitted.header = r.Header.Clone()
			submitted.body = doubaoRequest{}
			_ = json.NewDecoder(r.Body).Decode(&submitted.body)
			status = submitStatus
		} else {
			queried = r.Header.Clone()
		}
		w.Header().Set("X-Api-Status-Code", status)
		w.Header().Set("X-Api-Message", "OK")
		w.Header().Set("X-Tt-Logid", "log-1")
		switch {
		case status == "45000010":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":45000010,"message":"Invalid X-Api-Key"}`))
		case r.URL.Path == "/query" && status == "20000000":
			_, _ = w.Write([]byte(`{"audio_info":{"duration":5200},"result":{"text":"你好。我在。","utterances":[
{"start_time":100,"end_time":900,"text":"你好。","additions":{"speaker":"1"}},
{"start_time":1200,"end_time":2600,"text":"我在。","additions":{"speaker":2}},
{"start_time":3000,"end_time":3100,"text":"  "}]}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()
	relay := &memoryRelay{objects: map[string][]byte{}}
	storage := StorageCredentials{Region: "cn-beijing", Bucket: "family-voice", AccessKeyID: "AK", SecretAccessKey: "SK"}
	credentials := DoubaoCredentials{APIKey: "new-console-key", Storage: storage}
	doubao := Doubao{
		SubmitURL: server.URL + "/submit", QueryURL: server.URL + "/query",
		Credentials: func(context.Context) (DoubaoCredentials, error) { return credentials, nil },
		Relay:       func(s StorageCredentials) Relay { return bucketRelay{relay, s.Bucket} },
	}
	ctx := context.Background()
	task, err := doubao.Submit(ctx, []byte("OggS"), RecognizeRequest{Hotwords: []string{"张三", "张三", ""}, Duration: 40 * time.Minute, Scene: "meeting"})
	if err != nil {
		t.Fatal(err)
	}
	if len(task.ID) != 36 || task.Object != "a-nas-voice/"+task.ID+".ogg" || task.Bucket != "family-voice" || task.Region != "cn-beijing" ||
		string(relay.objects["family-voice/"+task.Object]) != "OggS" {
		t.Fatalf("task = %+v, relayed = %v", task, relay.objects)
	}
	if submitted.header.Get("X-Api-Key") != "new-console-key" || submitted.header.Get("X-Api-Resource-Id") != "volc.seedasr.auc" ||
		submitted.header.Get("X-Api-Request-Id") != task.ID || submitted.header.Get("X-Api-Sequence") != "-1" || submitted.header.Get("X-Api-App-Key") != "" {
		t.Fatalf("submit headers = %v", submitted.header)
	}
	body := submitted.body
	if body.Audio.URL != "https://family-voice.relay.test/"+task.Object+"?X-Tos-Signature=signed" || body.Audio.Format != "ogg" || body.Audio.Codec != "opus" ||
		body.Audio.Rate != 16000 || !body.Request.EnableSpeakerInfo || body.Request.SSDVersion != "300" || body.Request.SSDMode != 0 ||
		!body.Request.ShowUtterances || body.Request.Corpus == nil || body.Request.Corpus.Context != `{"hotwords":[{"word":"张三"}]}` {
		t.Fatalf("submit body = %+v", body)
	}
	if _, err := doubao.Result(ctx, task); !errors.Is(err, ErrRecognizing) || queried.Get("X-Api-Request-Id") != task.ID || queried.Get("X-Api-Key") != "new-console-key" {
		t.Fatalf("while processing: %v, headers %v", err, queried)
	}
	mu.Lock()
	queryStatus = "20000000"
	mu.Unlock()
	result, err := doubao.Result(ctx, task)
	if err != nil || len(result.Utterances) != 2 || result.Utterances[0].Speaker != "1" || result.Utterances[1].Speaker != "2" || result.Utterances[1].EndMs != 2600 {
		t.Fatalf("Result() = %+v, %v", result, err)
	}
	for code, want := range map[string]error{"20000002": ErrRecognizing, "45000010": ErrRecognizerRejected, "55000031": ErrRecognizerBusy,
		"45000131": ErrRecognizerBusy, "45000151": ErrNotAudio, "45000001": ErrRecognitionLost, "45000099": ErrRecognitionFailed} {
		mu.Lock()
		queryStatus = code
		mu.Unlock()
		if _, err := doubao.Result(ctx, task); !errors.Is(err, want) {
			t.Errorf("status %s: error = %v, want %v", code, err, want)
		}
	}
	mu.Lock()
	queryStatus = "20000003"
	mu.Unlock()
	if result, err := doubao.Result(ctx, task); err != nil || len(result.Utterances) != 0 {
		t.Fatalf("silence = %+v, %v", result, err)
	}
	// The relayed audio is deleted from the bucket it was put in, even after
	// an administrator chose another bucket.
	credentials.Storage.Bucket = "other-bucket"
	if err := doubao.Release(ctx, task); err != nil || len(relay.deleted) != 1 || relay.deleted[0] != "family-voice/"+task.Object {
		t.Fatalf("Release() = %v, deleted %v", err, relay.deleted)
	}
	// The old console's keys; a language the service separates no speakers in.
	credentials = DoubaoCredentials{AppID: "app", AccessToken: "token", Storage: storage}
	if _, err := doubao.Submit(ctx, []byte("OggS"), RecognizeRequest{Language: "en-US", Duration: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if submitted.header.Get("X-Api-App-Key") != "app" || submitted.header.Get("X-Api-Access-Key") != "token" || submitted.body.Audio.Language != "en-US" ||
		submitted.body.Request.EnableSpeakerInfo || submitted.body.Request.SSDVersion != "" {
		t.Fatalf("old console request = %v %+v", submitted.header, submitted.body)
	}
	// A refused submission deletes what it relayed.
	mu.Lock()
	submitStatus = "45000010"
	mu.Unlock()
	if _, err := doubao.Submit(ctx, []byte("OggS"), RecognizeRequest{}); !errors.Is(err, ErrRecognizerRejected) {
		t.Fatalf("refused submission = %v", err)
	}
	for key := range relay.objects {
		if key == "family-voice/a-nas-voice/"+submitted.header.Get("X-Api-Request-Id")+".ogg" {
			t.Fatalf("the refused task's audio stayed: %v", relay.objects)
		}
	}
	// Without a relay bucket the recognizer is not configured.
	credentials = DoubaoCredentials{APIKey: "key"}
	if _, err := doubao.Submit(ctx, []byte("OggS"), RecognizeRequest{}); !errors.Is(err, ErrRecognizerUnconfigured) || doubao.Ready(ctx) {
		t.Fatalf("no bucket = %v", err)
	}
}

func TestSpeakerSeparationFollowsDoubaosBestPractice(t *testing.T) {
	for _, c := range []struct {
		request RecognizeRequest
		version string
		mode    int
	}{
		{RecognizeRequest{Duration: 2 * time.Minute, Scene: "meeting"}, "200", 0},
		{RecognizeRequest{Duration: 20 * time.Minute, Scene: "call"}, "200", 1},
		{RecognizeRequest{Duration: 20 * time.Minute, Scene: "memo"}, "200", 1},
		{RecognizeRequest{Duration: 20 * time.Minute, Scene: "lecture"}, "300", 0},
		{RecognizeRequest{Duration: 20 * time.Minute}, "300", 0},
	} {
		if version, mode := speakerSeparation(c.request); version != c.version || mode != c.mode {
			t.Errorf("speakerSeparation(%+v) = %s/%d, want %s/%d", c.request, version, mode, c.version, c.mode)
		}
	}
}

func TestTOSRelaysThroughABucketWithPresignedURLs(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	objects := map[string][]byte{}
	refuse := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		signed := r.Header.Get("Authorization") != "" || r.URL.Query().Get("X-Tos-Signature") != ""
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("X-Tos-Request-Id", "req-1")
		if refuse || !signed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"Code":"AccessDenied","Message":"Access Denied","RequestId":"req-1"}`))
			return
		}
		switch r.Method {
		case http.MethodPut:
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
		case http.MethodGet:
			_, _ = w.Write(objects[r.URL.Path])
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	relay := TOS{Credentials: StorageCredentials{Region: "cn-beijing", Bucket: "family-voice", AccessKeyID: "AK", SecretAccessKey: "SK", Endpoint: server.URL}}
	ctx := context.Background()
	url, err := relay.Put(ctx, "a-nas-voice/x.ogg", []byte("OggS"))
	if err != nil {
		t.Fatal(err)
	}
	if string(objects["/family-voice/a-nas-voice/x.ogg"]) != "OggS" || !strings.HasPrefix(url, server.URL+"/family-voice/a-nas-voice/x.ogg?") ||
		!strings.Contains(url, "X-Tos-Signature=") || !strings.Contains(url, "X-Tos-Expires=86400") {
		t.Fatalf("Put() = %s, objects %v", url, objects)
	}
	// The presigned URL reads the audio without credentials, as Doubao does.
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	downloaded, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(downloaded) != "OggS" {
		t.Fatalf("downloaded %q", downloaded)
	}
	if err := relay.Delete(ctx, "a-nas-voice/x.ogg"); err != nil || len(objects) != 0 {
		t.Fatalf("Delete() = %v, objects %v", err, objects)
	}
	mu.Lock()
	refuse = true
	mu.Unlock()
	if _, err := relay.Put(ctx, "a-nas-voice/y.ogg", []byte("OggS")); !errors.Is(err, ErrRelayRejected) || !errors.Is(err, ErrRecognizerRejected) {
		t.Fatalf("refused Put() = %v", err)
	}
	server.Close()
	if _, err := relay.Put(ctx, "a-nas-voice/z.ogg", []byte("OggS")); !errors.Is(err, ErrRecognizerBusy) {
		t.Fatalf("unreachable Put() = %v", err)
	}
}

func TestConnectionTestRelaysASecondOfSilence(t *testing.T) {
	queryStatus := "20000003"
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("X-Api-Status-Code", "20000000")
		if r.URL.Path == "/query" {
			w.Header().Set("X-Api-Status-Code", queryStatus)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	relay := &memoryRelay{objects: map[string][]byte{}}
	f := newFixture(t, Options{})
	f.service.recognizer = Doubao{
		SubmitURL: server.URL + "/submit", QueryURL: server.URL + "/query", Credentials: f.store.DoubaoCredentials,
		Relay: func(s StorageCredentials) Relay { return bucketRelay{relay, s.Bucket} },
	}
	credentials := &RecognizerCredentials{Mode: ModeAPIKey, APIKey: "key", Storage: StorageInput{Region: "cn-beijing", Bucket: "family-voice", AccessKeyID: "AK", SecretAccessKey: "SK"}}
	if _, err := f.service.TestRecognizer(context.Background(), alice, credentials); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a member tests = %v", err)
	}
	result, err := f.service.TestRecognizer(context.Background(), bob, credentials)
	if err != nil || !result.OK || result.Queued || len(relay.objects) != 0 || len(relay.deleted) != 1 {
		t.Fatalf("TestRecognizer() = %+v, %v; relayed %v, deleted %v", result, err, relay.objects, relay.deleted)
	}
	// A long queue still proves the keys and the bucket.
	saved := testWait
	testWait = 20 * time.Millisecond
	t.Cleanup(func() { testWait = saved })
	mu.Lock()
	queryStatus = "20000002"
	mu.Unlock()
	if result, err := f.service.TestRecognizer(context.Background(), bob, credentials); err != nil || !result.OK || !result.Queued || len(relay.deleted) != 2 {
		t.Fatalf("queued test = %+v, %v", result, err)
	}
	// A relay that refuses the access key is named as the cause.
	f.service.recognizer = Doubao{
		SubmitURL: server.URL + "/submit", QueryURL: server.URL + "/query", Credentials: f.store.DoubaoCredentials,
		Relay: func(StorageCredentials) Relay { return refusingRelay{} },
	}
	if result, err := f.service.TestRecognizer(context.Background(), bob, credentials); err != nil || result.OK || result.Code != "storage_rejected" {
		t.Fatalf("refused relay = %+v, %v", result, err)
	}
}

type refusingRelay struct{}

func (refusingRelay) Put(context.Context, string, []byte) (string, error) {
	return "", fmt.Errorf("%w: %w: TOS AccessDenied", ErrRecognizerRejected, ErrRelayRejected)
}

func (refusingRelay) Delete(context.Context, string) error { return nil }

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
