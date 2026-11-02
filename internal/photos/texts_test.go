package photos_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos"
)

// readingEmbedder is the colour Worker with a text reader: every photo it is
// asked to read is a receipt, whose fields it reads with a template.
type readingEmbedder struct {
	*colorEmbedder
	mu    sync.Mutex
	model string
	reads []string
	// block makes a reading wait until it is cancelled, and say so.
	block   bool
	started chan struct{}
}

func newReadingEmbedder() *readingEmbedder {
	return &readingEmbedder{colorEmbedder: &colorEmbedder{}, model: "reader-1"}
}

func (r *readingEmbedder) Info(ctx context.Context) (photos.EmbedderInfo, error) {
	info, err := r.colorEmbedder.Info(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	info.OCR, info.OCRMemoryBytes = r.model, 1<<30
	return info, err
}

const receiptText = "某某超市\n2024年05月06日\n合计 ¥35.50\n实收 50.00"

func (r *readingEmbedder) ReadText(ctx context.Context, _ *os.File, schema json.RawMessage) (string, photos.Reading, error) {
	r.mu.Lock()
	model, block, started := r.model, r.block, r.started
	kind := "text"
	if schema != nil {
		kind = "fields:" + string(schema)
	}
	r.reads = append(r.reads, kind)
	r.mu.Unlock()
	if block {
		close(started)
		<-ctx.Done()
		return "", photos.Reading{}, ctx.Err()
	}
	if schema == nil {
		return model, photos.Reading{Text: receiptText, Finish: "stop"}, nil
	}
	answer := `{"商户": "某某超市", "日期": "2024年05月06日", "合计": "¥35.80"}`
	return model, photos.Reading{Text: answer, Finish: "stop", Tokens: []photos.TokenProb{{Token: answer}}}, nil
}

func (r *readingEmbedder) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reads)
}

type storedText struct {
	model, kind, text string
	fields            map[string]string
	checks            map[string]struct {
		Passed  bool     `json:"passed"`
		Reasons []string `json:"reasons"`
	}
}

func readings(t *testing.T, root string) []storedText {
	t.Helper()
	rows, err := catalogDB(t, root).Query("SELECT model, kind, text, fields, checks FROM texts ORDER BY object_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var found []storedText
	for rows.Next() {
		var text storedText
		var fields, checks string
		if err := rows.Scan(&text.model, &text.kind, &text.text, &fields, &checks); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(fields), &text.fields); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(checks), &text.checks); err != nil {
			t.Fatal(err)
		}
		found = append(found, text)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

func TestRoutedPhotosAreReadTypedAndChecked(t *testing.T) {
	embedder := newReadingEmbedder()
	root := filepath.Join(t.TempDir(), "photos")
	service := routeService(t, root, embedder, redRoute(t, "colour-1"))
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "receipt.png", red)
	importPhoto(t, service, namedAlice, private.ID, "", "sea.png", blue)
	index(t, service, embedder)

	found := readings(t, root)
	if len(found) != 1 {
		t.Fatalf("readings = %+v, want the red photo's only", found)
	}
	reading := found[0]
	if reading.model != "reader-1" || reading.kind != "receipt" || reading.text != receiptText {
		t.Fatalf("reading = %+v", reading)
	}
	// The whole text, then the receipt's fields with its template.
	if embedder.readCount() != 2 {
		t.Fatalf("reads = %v", embedder.reads)
	}
	if reading.fields["合计"] != "¥35.80" || reading.fields["商户"] != "某某超市" {
		t.Fatalf("fields = %v", reading.fields)
	}
	// 35.80 was not in the whole text, which said 35.50: a person looks.
	if !reading.checks["商户"].Passed || !reading.checks["日期"].Passed || reading.checks["合计"].Passed {
		t.Fatalf("checks = %+v", reading.checks)
	}
	if got := textPhotos(t, service, namedAlice); got == nil || *got != 1 {
		t.Fatalf("TextPhotos = %s, want the photo read", shown(got))
	}

	// A new reader, which comes with a new release and so a restart, reads
	// the photo again.
	_ = service.Close()
	embedder.mu.Lock()
	embedder.model = "reader-2"
	embedder.mu.Unlock()
	service = routeService(t, root, embedder, redRoute(t, "colour-1"))
	index(t, service, embedder)
	if found := readings(t, root); len(found) != 1 || found[0].model != "reader-2" {
		t.Fatalf("readings after a new reader = %+v", found)
	}
}

// gateSwitch is a gate the test closes while a reading runs.
type gateSwitch struct{ closed atomic.Bool }

func (g *gateSwitch) Open(context.Context, uint64) (bool, string) {
	if g.closed.Load() {
		return false, "foreground"
	}
	return true, ""
}

func TestAReadingStopsWhenTheGateClosesAndKeepsItsAttempt(t *testing.T) {
	embedder := newReadingEmbedder()
	root := filepath.Join(t.TempDir(), "photos")
	service := routeService(t, root, embedder, redRoute(t, "colour-1"))
	private, _ := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "receipt.png", red)
	processAll(t, service)
	gate := &gateSwitch{}
	// Vectors and the route first, then the reading blocks.
	embedder.mu.Lock()
	embedder.block, embedder.started = true, make(chan struct{})
	started := embedder.started
	embedder.mu.Unlock()
	go func() {
		<-started
		gate.closed.Store(true)
	}()
	processAI(t, service, embedder, gate)

	if embedder.readCount() != 1 {
		t.Fatalf("reads before the gate closed = %v, want the one that blocked", embedder.reads)
	}
	if found := readings(t, root); len(found) != 0 {
		t.Fatalf("readings = %+v", found)
	}
	var state string
	var attempts int
	if err := catalogDB(t, root).QueryRow("SELECT state, attempts FROM jobs WHERE derivation = 'ocr/v1'").Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || attempts != 0 {
		t.Fatalf("the stopped reading's job is %s after %d attempts, want pending after none", state, attempts)
	}

	// The gate opens again: the photo is read.
	gate.closed.Store(false)
	embedder.mu.Lock()
	embedder.block = false
	embedder.mu.Unlock()
	processAI(t, service, embedder, gate)
	if found := readings(t, root); len(found) != 1 {
		t.Fatalf("readings once the gate opened = %+v", found)
	}
}

func TestTurningReadingOffClearsReadingsAndKeepsCorrections(t *testing.T) {
	embedder := newReadingEmbedder()
	root := filepath.Join(t.TempDir(), "photos")
	service := routeService(t, root, embedder, redRoute(t, "colour-1"))
	ctx := context.Background()
	private, shared := libraries(t, service, namedAlice)
	importPhoto(t, service, namedAlice, private.ID, "", "receipt.png", red)
	index(t, service, embedder)
	db := catalogDB(t, root)
	var objectID string
	if err := db.QueryRow("SELECT object_id FROM texts").Scan(&objectID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO text_corrections(library_id, object_id, field, value, account, updated_at)
VALUES(?, ?, '合计', '¥35.50', 'alice', '2026-11-02T00:00:00Z')`, private.ID, objectID); err != nil {
		t.Fatal(err)
	}

	// Only the owner of a private library, an administrator for the shared one.
	if _, err := service.SetTexts(ctx, namedAlice, shared.ID, false); !errors.Is(err, photos.ErrForbidden) {
		t.Fatalf("a member turning the shared library off: error = %v", err)
	}
	library, err := service.SetTexts(ctx, namedAlice, private.ID, false)
	if err != nil || library.Texts || !library.People {
		t.Fatalf("SetTexts(off) = %+v, %v", library, err)
	}
	if found := readings(t, root); len(found) != 0 {
		t.Fatalf("readings after turning off = %+v", found)
	}
	var corrections int
	if err := db.QueryRow("SELECT COUNT(*) FROM text_corrections").Scan(&corrections); err != nil || corrections != 1 {
		t.Fatalf("corrections after turning off = %d, %v", corrections, err)
	}
	// Off, nothing is read.
	index(t, service, embedder)
	if found := readings(t, root); len(found) != 0 {
		t.Fatalf("readings while off = %+v", found)
	}

	if _, err := service.SetTexts(ctx, namedAlice, private.ID, true); err != nil {
		t.Fatal(err)
	}
	index(t, service, embedder)
	if found := readings(t, root); len(found) != 1 {
		t.Fatalf("readings after turning on again = %+v", found)
	}
}
