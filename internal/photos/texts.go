package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
)

// Reading the text in photos (docs/architecture/photo-ocr.md): the photos
// routed for it (textrouting.go) are read in a phase of their own, after the
// other AI work, the newest photo first. A reading is derived and kept per
// content object; its scan records the model, so that a new model reads
// again. Each library can turn reading off, as with people: the owner of a
// private library, an administrator for the shared one.

// inTextLibrary is the condition that the object whose id is the SQL
// expression object has a photo, trashed or not, in a library with reading
// on.
func inTextLibrary(object string) string {
	return "EXISTS (SELECT 1 FROM assets ta JOIN libraries tl ON tl.id = ta.library_id WHERE ta.object_id = " +
		object + " AND tl.texts = 1)"
}

func textsOn(ctx context.Context, q queryer, libraryID string) (bool, error) {
	var on bool
	err := q.QueryRowContext(ctx, "SELECT texts FROM libraries WHERE id = ?", libraryID).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return on, err
}

// claimTextJob leases the due reading of the newest photo: the most recent
// receipts and cards are the ones most often looked for.
func (s *Service) claimTextJob(ctx context.Context) (claimedJob, error) {
	return s.claimJobIn(ctx, textDerivation, "",
		"(SELECT MAX(IFNULL(a.taken_at, a.imported_at)) FROM assets a WHERE a.object_id = j.object_id) DESC, j.object_id")
}

// textReading is what one round read from a photo.
type textReading struct {
	model string
	kind  textread.Kind
	text  string
	// finish is how the whole text ended (Reading.Finish): "stop", or
	// "length" and "repetition" for a text cut off or stopped looping, which
	// is kept as read.
	finish string
	fields map[string]string
	checks map[string]textread.Check
}

// enqueueTextReading queues the reading of a photo newly in a library that
// reads text: a copy or an import of an original that so far only libraries
// with reading off held, whose stored vector found it then but did not queue
// it. Until the route's query vectors are loaded, the stored vectors are
// routed later anyway (routeStoredVectors).
func (s *Service) enqueueTextReading(ctx context.Context, tx *sql.Tx, objectID string) error {
	s.routeIndex.mu.Lock()
	queries := s.routeIndex.vectors
	s.routeIndex.mu.Unlock()
	if queries == nil {
		return nil
	}
	var encoded []byte
	err := tx.QueryRowContext(ctx, "SELECT vector FROM embeddings WHERE object_id = ? AND derivation = ? AND model = ?",
		objectID, embeddingDerivation, s.textRoute.Model).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		// Not embedded yet: routed when its vector is stored.
		return nil
	}
	if err != nil || !s.hasText(decodeVector(encoded), queries) {
		return err
	}
	return enqueueText(ctx, tx, objectID, formatTime(s.now()))
}

// completeText stores a reading and retires its job.
func (s *Service) completeText(ctx context.Context, job claimedJob, reading textReading) error {
	fields, err := json.Marshal(reading.fields)
	if err != nil {
		return err
	}
	checks, err := json.Marshal(reading.checks)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE object_id = ? AND derivation = ?)",
			job.objectID, job.derivation).Scan(&exists); err != nil || !exists {
			// Purged, or its library turned reading off, while the Worker read.
			return err
		}
		now := formatTime(s.now())
		if _, err := tx.ExecContext(ctx, `
INSERT INTO texts(object_id, model, kind, text, finish, fields, checks, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(object_id) DO UPDATE SET model = excluded.model, kind = excluded.kind, text = excluded.text,
  finish = excluded.finish, fields = excluded.fields, checks = excluded.checks, created_at = excluded.created_at`,
			job.objectID, reading.model, string(reading.kind), reading.text, reading.finish, string(fields), string(checks),
			now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO scans(object_id, derivation, model, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(object_id, derivation) DO UPDATE SET model = excluded.model, created_at = excluded.created_at`,
			job.objectID, textDerivation, reading.model, now); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM jobs WHERE object_id = ? AND derivation = ?", job.objectID, job.derivation)
		return err
	})
}

// requeueStaleTexts queues again the photos read by another model than the
// Worker's reader, in libraries that still read text, and with retryFailed,
// for a reader the photo service has not seen before, the photos whose
// reading failed.
func (s *Service) requeueStaleTexts(ctx context.Context, model string, retryFailed bool) error {
	now := formatTime(s.now())
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO jobs(object_id, derivation, state, not_before, created_at)
SELECT sc.object_id, sc.derivation, 'pending', ?, ? FROM scans sc
WHERE sc.derivation = ? AND sc.model <> ? AND `+inTextLibrary("sc.object_id")+`
ON CONFLICT(object_id, derivation) DO NOTHING`, now, now, textDerivation, model); err != nil {
		return err
	}
	if !retryFailed {
		return nil
	}
	// With retryFailed, a new reader also tries the photos the last one
	// failed on or answered badly; a photo it cannot decode stays failed.
	_, err := s.db.ExecContext(ctx, `
UPDATE jobs SET state = 'pending', attempts = 0, lease_until = NULL, not_before = ?, error_class = ''
WHERE derivation = ? AND state = 'failed' AND error_class IN (?, ?)`,
		now, textDerivation, jobErrorIO, jobErrorInvalidResult)
	return err
}

// SetTexts turns reading the text in photos on or off for a library.
func (s *Service) SetTexts(ctx context.Context, p Principal, libraryID string, on bool) (Library, error) {
	return s.SetSwitches(ctx, p, libraryID, LibrarySwitches{Texts: &on})
}

// switchTexts turns reading the text in photos on or off. Off clears the
// readings of the photos no library that reads still holds and keeps what
// members corrected; on routes the stored vectors again (SetSwitches).
func (s *Service) switchTexts(ctx context.Context, tx *sql.Tx, p Principal, lib library, on bool, done *switched) error {
	if was, err := textsOn(ctx, tx, lib.id); err != nil || was == on {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE libraries SET texts = ? WHERE id = ?", on, lib.id); err != nil {
		return err
	}
	action := "library.texts_on"
	if on {
		done.textsOn = true
	} else {
		action = "library.texts_off"
		// The photos of the library that no library reading text still holds.
		gone := "SELECT a.object_id FROM assets a WHERE a.library_id = ? AND NOT " + inTextLibrary("a.object_id")
		for _, statement := range []string{
			"DELETE FROM texts WHERE object_id IN (" + gone + ")",
			"DELETE FROM scans WHERE derivation = '" + textDerivation + "' AND object_id IN (" + gone + ")",
			"DELETE FROM jobs WHERE derivation = '" + textDerivation + "' AND object_id IN (" + gone + ")",
		} {
			if _, err := tx.ExecContext(ctx, statement, lib.id); err != nil {
				return err
			}
		}
	}
	return s.audit(ctx, tx, p.UserID, action, lib.id, "")
}

// publicSwitches is the library as p sees it, with its AI switches.
func (s *Service) publicSwitches(ctx context.Context, p Principal, lib library) (Library, error) {
	public := publicLibrary(p, lib, s.now())
	var err error
	if public.People, err = peopleOn(ctx, s.db, lib.id); err != nil {
		return Library{}, err
	}
	if public.Texts, err = textsOn(ctx, s.db, lib.id); err != nil {
		return Library{}, err
	}
	public.CanSetPeople = canSetPeople(p, lib)
	return public, nil
}

// textMinConfidence is the token probability every token of a field's value
// must reach to pass (docs/architecture/photo-ocr.md#核对层). Measured with
// Qwen3.5-4B on 50 CC-OCR receipts and cards, the other checks passed 91% of
// the fields with 12% wrong; at 0.99, 48% pass with 2.7% wrong, most of them
// differing only in format, while 0.9 let a serial number one digit off pass
// (docs/research/photo-ocr-model-evaluation.md, 核对层实测).
const textMinConfidence = 0.99

// errReaderChanged: the Worker answered the two reads of one photo with
// different models.
var errReaderChanged = errors.New("the text reader changed between the reads of one photo")

// errNoEnd: the Worker gave a reading without one of the ways a reading ends
// (Reading.Finish), so its text cannot be taken as read (#365).
var errNoEnd = errors.New("the text reader gave a reading without saying how it ended")

// ended reports whether finish is one of the ways a reading ends: by itself,
// at the length limit, or stopped looping.
func ended(finish string) bool {
	return finish == "stop" || finish == "length" || finish == "repetition"
}

// readText reads the next routed photo, the newest first, once there is
// nothing to detect (docs/architecture/photo-ocr.md#资源策略): its whole
// text, and for a known document type its fields, which are then checked.
// The reader needs memory of its own, so the detection models go first and
// the gate weighs the reader's need. When gate closes meanwhile, the Worker
// is hung up on and the job goes back without spending its attempt.
//
// claimed is false only when no photo waits to be read, and the caller goes
// on to idle; a photo claimed ends the round, read or not, as a detection
// does: a closed gate's pause stands, and the models stay as it left them.
func (r *aiRunner) readText(ctx context.Context, m models, gate Gate, info EmbedderInfo) (claimed, processed bool, err error) {
	job, err := r.lib.claimTextJob(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	r.switchPhase(ctx, m.detector, true)
	if open, reason := gate.Open(ctx, info.OCRMemoryBytes); !open {
		r.progress.set(AIPaused, reason, "")
		r.releaseModels(ctx, m.detector, reason)
		return true, false, r.lib.returnJob(ctx, job)
	}
	r.progress.set(AIWorking, "", info.Model)
	file, class, err := r.lib.openAIInput(ctx, job)
	switch {
	case err != nil:
		return true, false, errors.Join(err, r.lib.releaseJob(ctx, job))
	case class == jobWaiting:
		return true, false, r.lib.releaseJob(ctx, job)
	case class != "":
		return true, true, r.lib.failJob(ctx, job, class)
	}
	defer func() { _ = file.Close() }()
	r.markReaderLoaded()

	readCtx, cancel := context.WithCancel(ctx)
	stopWatching := watchGate(readCtx, gate, info.OCRMemoryBytes, cancel)
	model, whole, err := m.reader.ReadText(readCtx, file, nil)
	if err == nil && !ended(whole.Finish) {
		err = errNoEnd
	}
	reading := textReading{model: model, kind: textread.Plain, text: whole.Text, finish: whole.Finish}
	if err == nil {
		reading.kind = textread.Classify(whole.Text)
		if template := textread.Template(reading.kind); template != nil {
			reading.fields, reading.checks, err = r.readFields(readCtx, m.reader, file, model, reading.kind, template, whole.Text)
		}
	}
	paused := stopWatching()
	cancel()
	switch {
	case ctx.Err() != nil:
		return true, false, r.lib.returnJob(context.WithoutCancel(ctx), job)
	case paused != "":
		r.progress.set(AIPaused, paused, "")
		err := r.lib.returnJob(ctx, job)
		r.releaseModels(ctx, m.detector, paused)
		return true, false, err
	case errors.Is(err, ErrAIUnavailable) || errors.Is(err, errReaderChanged) || (err == nil && model != info.OCR):
		// Look again next round: the Worker may have lost or changed its reader.
		r.forgetModels()
		return true, false, r.lib.releaseJob(ctx, job)
	case errors.Is(err, errNoEnd):
		return true, true, r.lib.failJob(ctx, job, jobErrorInvalidResult)
	case errors.Is(err, ErrAIRejected):
		return true, true, r.lib.failJob(ctx, job, jobErrorUndecodable)
	case err != nil:
		return true, true, r.lib.failJob(ctx, job, jobErrorIO)
	}
	return true, true, r.lib.completeText(ctx, job, reading)
}

// readFields reads a typed photo's fields with its type's template and
// checks them against the whole text.
func (r *aiRunner) readFields(ctx context.Context, reader TextReader, file *os.File, model string, kind textread.Kind,
	template json.RawMessage, whole string,
) (map[string]string, map[string]textread.Check, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, err
	}
	fieldsModel, answer, err := reader.ReadText(ctx, file, template)
	if err != nil {
		return nil, nil, err
	}
	if fieldsModel != model {
		return nil, nil, errReaderChanged
	}
	if !ended(answer.Finish) {
		return nil, nil, errNoEnd
	}
	tokens := make([]textread.Token, len(answer.Tokens))
	for i, token := range answer.Tokens {
		tokens[i] = textread.Token{Text: token.Token, LogProb: token.LogProb}
	}
	fields := textread.Fields(kind, answer.Text)
	options := textread.Options{MinConfidence: textMinConfidence, Unfinished: answer.Finish != "stop"}
	return fields, textread.Verify(kind, fields, whole, tokens, options), nil
}
