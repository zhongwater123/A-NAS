package family

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxRecordingBytes bounds one recording: two hours of phone-quality audio
// fit easily.
const MaxRecordingBytes = 200 << 20

const (
	maxRecordingTitleRunes = 60
	maxFileNameRunes       = 120
)

// recordingTypes are the audio formats a recording may have. Browsers play
// all but AMR and 3GPP, which phones' recorder apps still produce; those
// play through the media worker.
var recordingTypes = []string{
	"audio/mpeg", "audio/mp4", "audio/x-m4a", "audio/aac", "audio/wav", "audio/x-wav", "audio/wave",
	"audio/ogg", "audio/webm", "audio/flac", "audio/x-flac", "audio/amr", "audio/3gpp",
}

// NeedsConversion reports whether browsers cannot play a recording's format
// and it has to be converted to play.
func NeedsConversion(contentType string) bool {
	return contentType == "audio/amr" || contentType == "audio/3gpp"
}

// RecordingInput describes an uploaded recording.
type RecordingInput struct {
	RequestKey  string
	Title       string
	OccurredOn  string
	FileName    string
	ContentType string
}

// ErrTooLarge means an upload passed its size limit.
var ErrTooLarge = errors.New("too large")

func cleanContentType(value string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", false
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType, slices.Contains(recordingTypes, mediaType)
}

// AddRecording stores an uploaded recording and adds it to a baby's channel.
// The file is written beside the database first and renamed into place
// only when complete, so a failed upload leaves nothing behind.
func (s *Store) AddRecording(ctx context.Context, actor Actor, babyID string, input RecordingInput, body io.Reader) (Record, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Record{}, err
	}
	if _, err := s.baby(ctx, s.db, babyID); err != nil {
		return Record{}, err
	}
	if input.RequestKey != "" {
		if existing, err := s.byRequest(ctx, actor, input.RequestKey); err == nil {
			return existing, nil
		}
	}
	contentType, ok := cleanContentType(input.ContentType)
	if !ok {
		return Record{}, ErrInvalid
	}
	title, ok := validText(input.Title, maxRecordingTitleRunes, false)
	if !ok || len(input.RequestKey) > maxRequestKey {
		return Record{}, ErrInvalid
	}
	fileName := strings.TrimSpace(filepath.Base(strings.ReplaceAll(input.FileName, "\\", "/")))
	if !utf8.ValidString(fileName) || utf8.RuneCountInString(fileName) > maxFileNameRunes || fileName == "." {
		fileName = ""
	}
	occurredOn := input.OccurredOn
	if occurredOn == "" {
		occurredOn = s.today().Format(dateLayout)
	}
	if date, ok := parseDate(occurredOn); !ok || date.After(s.today()) {
		return Record{}, ErrInvalid
	}
	id := newID("rec")
	temporary, err := os.CreateTemp(s.recordings, ".upload-*")
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	size, err := io.Copy(temporary, io.LimitReader(body, MaxRecordingBytes+1))
	if err == nil && size > MaxRecordingBytes {
		err = ErrTooLarge
	}
	if err == nil && size == 0 {
		err = ErrInvalid
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Record{}, err
	}
	final := s.recordingPath(id)
	if err := os.Rename(temporary.Name(), final); err != nil {
		return Record{}, err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		if _, err := s.baby(ctx, tx, babyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO records(id, baby_id, kind, occurred_on, text, author_id, author_name, created_at,
request_key, recording_type, recording_bytes, recording_name) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, babyID, KindRecording, occurredOn, title, actor.AccountID, actor.Username, formatTime(s.now()),
			nullString(input.RequestKey), contentType, size, fileName); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return err
		}
		return s.audit(ctx, tx, actor, "family.record_added", id, string(KindRecording))
	})
	if err != nil {
		_ = os.Remove(final)
		if errors.Is(err, ErrConflict) && input.RequestKey != "" {
			return s.byRequest(ctx, actor, input.RequestKey)
		}
		return Record{}, err
	}
	return s.readRecord(ctx, s.db, actor, id, false)
}

func (s *Store) recordingPath(recordID string) string {
	return filepath.Join(s.recordings, recordID)
}

// OpenRecording opens a recording's audio for a member to play.
func (s *Store) OpenRecording(ctx context.Context, actor Actor, recordID string) (*os.File, Recording, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return nil, Recording{}, err
	}
	stored, err := s.scanStored(ctx, s.db, recordID)
	if err != nil {
		return nil, Recording{}, err
	}
	if stored.record.Kind != KindRecording {
		return nil, Recording{}, ErrNotFound
	}
	if _, err := s.baby(ctx, s.db, stored.record.BabyID); err != nil {
		return nil, Recording{}, err
	}
	file, err := os.Open(s.recordingPath(recordID))
	if err != nil {
		return nil, Recording{}, fmt.Errorf("open recording: %w", err)
	}
	return file, *stored.record.Recording, nil
}

// Purge deletes for good what has been in the trash for TrashDays: records
// and babies with their channels, and the recordings they held.
func (s *Store) Purge(ctx context.Context) error {
	cutoff := s.trashCutoff()
	var recordings []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT r.id FROM records r JOIN babies b ON b.id = r.baby_id
WHERE r.kind = 'recording' AND ((r.deleted_at IS NOT NULL AND r.deleted_at <= ?) OR (b.deleted_at IS NOT NULL AND b.deleted_at <= ?))`,
			cutoff, cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			recordings = append(recordings, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM records WHERE deleted_at IS NOT NULL AND deleted_at <= ?", cutoff); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM babies WHERE deleted_at IS NOT NULL AND deleted_at <= ?", cutoff)
		return err
	})
	if err != nil {
		return err
	}
	for _, id := range recordings {
		if err := os.Remove(s.recordingPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return s.removeStrayUploads()
}

// removeStrayUploads deletes uploads a crash left half-written.
func (s *Store) removeStrayUploads() error {
	entries, err := os.ReadDir(s.recordings)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".upload-") {
			if err := os.Remove(filepath.Join(s.recordings, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
