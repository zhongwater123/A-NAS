package photos

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
)

// The text read from a photo, as members see and correct it
// (docs/architecture/photo-ocr.md, ADR 0033). A correction belongs to the
// content object, whichever library a member made it in, and the last one
// stands; every change is kept, with who made it from which library, for
// everyone who can see the photo. A member's AI assistant sees the text with
// every sensitive value replaced by a handle (textread.Sensitive,
// textread.Numbers) and changes nothing.

// ErrInvalidText refuses a correction that is too long or holds control
// characters.
var ErrInvalidText = errors.New("invalid text correction")

// MaxCorrectionRunes bounds a corrected field's value.
const MaxCorrectionRunes = 200

// Text states.
const (
	// TextRead: the photo's text was read.
	TextRead = "read"
	// TextQueued: the photo waits to be read.
	TextQueued = "queued"
	// TextFailed: reading the photo failed.
	TextFailed = "failed"
	// TextNone: the photo is not one whose text is read.
	TextNone = "none"
	// TextOff: the library the photo is in does not read text.
	TextOff = "off"
)

// PhotoText is a photo's text as the caller sees it.
type PhotoText struct {
	Status string `json:"status"`
	// Kind is the document type read, or plain; the rest is set only when
	// Status is read.
	Kind string `json:"kind,omitempty"`
	// Text is the whole text as read, which machine reading may have got
	// wrong.
	Text string `json:"text,omitempty"`
	// Complete is false when the reading was cut off at its length limit or
	// stopped looping.
	Complete bool        `json:"complete"`
	Model    string      `json:"model,omitempty"`
	Fields   []TextField `json:"fields"`
}

// TextField is one field of a document type, in the order the document
// shows them.
type TextField struct {
	Name string `json:"name"`
	// Value is what the field holds: the correction, or else what was read.
	Value string `json:"value"`
	// Read is what was read, empty when nothing was.
	Read string `json:"read"`
	// Passed says the reading passed every check; Reasons say why not.
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons,omitempty"`
	// Sensitive values are shown masked until the viewer opens them.
	Sensitive  bool            `json:"sensitive"`
	Correction *TextCorrection `json:"correction,omitempty"`
}

// TextCorrection says who last corrected a field, and when.
type TextCorrection struct {
	UserID   string    `json:"userId"`
	Username string    `json:"username"`
	At       time.Time `json:"at"`
}

// TextChange is one change to a field: a correction, or a correction removed
// so that the field shows what was read again.
type TextChange struct {
	Field    string `json:"field"`
	Old      string `json:"old"`
	New      string `json:"new"`
	Restored bool   `json:"restored"`
	UserID   string `json:"userId"`
	Username string `json:"username"`
	// Library is the one the change was made from; only its ID is left
	// once it is gone.
	Library TextChangeLibrary `json:"library"`
	At      time.Time         `json:"at"`
}

// TextChangeLibrary is the library a change was made from.
type TextChangeLibrary struct {
	ID        string      `json:"id"`
	Kind      LibraryKind `json:"kind,omitempty"`
	OwnerName string      `json:"ownerName,omitempty"`
}

// storedReading is a photo's row of texts, with its corrections.
type storedReading struct {
	model, kind, text, finish string
	fields                    map[string]string
	checks                    map[string]textread.Check
	corrections               map[string]correction
}

type correction struct {
	value, account, username string
	at                       time.Time
}

// Text returns a photo's text as p sees it. A library that does not read
// text shows none, even of a photo another library holds and reads.
func (s *Service) Text(ctx context.Context, p Principal, assetID string) (PhotoText, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return PhotoText{}, err
	}
	return s.photoText(ctx, p, record)
}

func (s *Service) photoText(ctx context.Context, p Principal, record assetRecord) (PhotoText, error) {
	on, err := textsOn(ctx, s.db, record.library.id)
	if err != nil {
		return PhotoText{}, err
	}
	if !on {
		return PhotoText{Status: TextOff, Fields: []TextField{}}, nil
	}
	reading, found, err := s.storedReading(ctx, s.db, record.objectID)
	if err != nil {
		return PhotoText{}, err
	}
	if !found {
		status, err := s.textStatus(ctx, record.objectID)
		return PhotoText{Status: status, Fields: []TextField{}}, err
	}
	text := PhotoText{Status: TextRead, Kind: reading.kind, Text: reading.text, Complete: reading.finish == "stop",
		Model: reading.model, Fields: []TextField{}}
	for _, name := range textread.FieldNames(textread.Kind(reading.kind)) {
		_, sensitive := textread.Sensitive(name)
		field := TextField{Name: name, Read: reading.fields[name], Value: reading.fields[name], Sensitive: sensitive}
		if check, ok := reading.checks[name]; ok {
			field.Passed, field.Reasons = check.Passed, check.Reasons
		}
		if corrected, ok := reading.corrections[name]; ok {
			field.Value = corrected.value
			field.Correction = &TextCorrection{UserID: corrected.account, Username: corrected.username, At: corrected.at}
		}
		text.Fields = append(text.Fields, field)
	}
	if p.Assistant {
		return s.maskForAssistant(ctx, p, record, text)
	}
	return text, nil
}

// textStatus says where a photo without a reading stands.
func (s *Service) textStatus(ctx context.Context, objectID string) (string, error) {
	var state string
	err := s.db.QueryRowContext(ctx, "SELECT state FROM jobs WHERE object_id = ? AND derivation = ?",
		objectID, textDerivation).Scan(&state)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TextNone, nil
	case err != nil:
		return "", err
	case state == "failed":
		return TextFailed, nil
	default:
		return TextQueued, nil
	}
}

// storedReading reads an object's reading and its corrections.
func (s *Service) storedReading(ctx context.Context, q queryer, objectID string) (storedReading, bool, error) {
	var reading storedReading
	var fields, checks string
	err := q.QueryRowContext(ctx, "SELECT model, kind, text, finish, fields, checks FROM texts WHERE object_id = ?",
		objectID).Scan(&reading.model, &reading.kind, &reading.text, &reading.finish, &fields, &checks)
	if errors.Is(err, sql.ErrNoRows) {
		return storedReading{}, false, nil
	}
	if err != nil {
		return storedReading{}, false, err
	}
	if err := errors.Join(json.Unmarshal([]byte(fields), &reading.fields),
		json.Unmarshal([]byte(checks), &reading.checks)); err != nil {
		return storedReading{}, false, err
	}
	reading.corrections, err = s.corrections(ctx, q, objectID)
	return reading, true, err
}

// corrections reads an object's corrections, each with its author's name
// when making it.
func (s *Service) corrections(ctx context.Context, q queryer, objectID string) (map[string]correction, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT field, value, account, account_name, updated_at FROM text_corrections WHERE object_id = ?", objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	corrections := map[string]correction{}
	for rows.Next() {
		var field, at string
		var corrected correction
		if err := rows.Scan(&field, &corrected.value, &corrected.account, &corrected.username, &at); err != nil {
			return nil, err
		}
		if corrected.at, err = parseTime(at); err != nil {
			return nil, err
		}
		corrections[field] = corrected
	}
	return corrections, rows.Err()
}

// CorrectText sets a field of a photo's reading to value, for every library
// that holds the photo, and returns the photo's text. Anyone who sees the
// photo may, except through a viewing grant or as an assistant; the photo
// must have a reading of a type that has the field, in a library that reads
// text, and must not be in the trash.
func (s *Service) CorrectText(ctx context.Context, p Principal, assetID, field, value string) (PhotoText, error) {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) > MaxCorrectionRunes || !utf8.ValidString(value) ||
		strings.ContainsFunc(value, unicode.IsControl) {
		return PhotoText{}, ErrInvalidText
	}
	return s.changeText(ctx, p, assetID, field, func(storedReading) (string, bool) { return value, false })
}

// RestoreText removes the correction of a field, which shows what was read
// again, and returns the photo's text.
func (s *Service) RestoreText(ctx context.Context, p Principal, assetID, field string) (PhotoText, error) {
	return s.changeText(ctx, p, assetID, field, func(reading storedReading) (string, bool) {
		return reading.fields[field], true
	})
}

// changeText applies one change to a field and records it; next gives the
// field's new value and whether it removes the correction.
func (s *Service) changeText(ctx context.Context, p Principal, assetID, field string,
	next func(storedReading) (string, bool),
) (PhotoText, error) {
	var record assetRecord
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		if record, err = s.visibleAsset(ctx, tx, p, assetID); err != nil {
			return err
		}
		if p.Assistant || viewingOnly(p, record.library) {
			return ErrForbidden
		}
		if record.Trash != nil {
			return ErrConflict
		}
		on, err := textsOn(ctx, tx, record.library.id)
		if err != nil {
			return err
		}
		reading, found, err := s.storedReading(ctx, tx, record.objectID)
		if err != nil {
			return err
		}
		if !on || !found {
			return ErrConflict
		}
		if !slices.Contains(textread.FieldNames(textread.Kind(reading.kind)), field) {
			return ErrNotFound
		}
		old := reading.fields[field]
		corrected, wasCorrected := reading.corrections[field]
		if wasCorrected {
			old = corrected.value
		}
		value, restore := next(reading)
		switch {
		case restore && !wasCorrected:
			return ErrNotFound
		case !restore && wasCorrected && value == old:
			return nil
		}
		now := formatTime(s.now())
		if restore {
			_, err = tx.ExecContext(ctx, "DELETE FROM text_corrections WHERE object_id = ? AND field = ?", record.objectID, field)
		} else {
			_, err = tx.ExecContext(ctx, `
INSERT INTO text_corrections(object_id, field, value, account, account_name, updated_at) VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(object_id, field) DO UPDATE SET value = excluded.value, account = excluded.account,
  account_name = excluded.account_name, updated_at = excluded.updated_at`,
				record.objectID, field, value, p.UserID, p.Username, now)
		}
		if err != nil {
			return err
		}
		// The text itself goes in no activity record: only this change log
		// keeps it, beside the photo.
		_, err = tx.ExecContext(ctx, `
INSERT INTO text_changes(object_id, field, old_value, new_value, restored, account, account_name, library_id, created_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.objectID, field, old, value, restore, p.UserID, p.Username,
			record.library.id, now)
		return err
	})
	if err != nil {
		return PhotoText{}, err
	}
	return s.photoText(ctx, p, record)
}

// TextChanges lists the changes to a photo's fields, newest first, for
// anyone who sees the photo in a library that reads text. An assistant gets
// none: they hold the values it may not know.
func (s *Service) TextChanges(ctx context.Context, p Principal, assetID string) ([]TextChange, error) {
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return nil, err
	}
	if p.Assistant {
		return nil, ErrForbidden
	}
	if on, err := textsOn(ctx, s.db, record.library.id); err != nil || !on {
		if err == nil {
			err = ErrConflict
		}
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT ch.field, ch.old_value, ch.new_value, ch.restored, ch.account, ch.account_name, ch.library_id,
  IFNULL(l.kind, ''), IFNULL(l.owner_name, ''), ch.created_at
FROM text_changes ch LEFT JOIN libraries l ON l.id = ch.library_id
WHERE ch.object_id = ? ORDER BY ch.id DESC`, record.objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []TextChange{}
	for rows.Next() {
		var change TextChange
		var at string
		if err := rows.Scan(&change.Field, &change.Old, &change.New, &change.Restored, &change.UserID, &change.Username,
			&change.Library.ID, &change.Library.Kind, &change.Library.OwnerName, &at); err != nil {
			return nil, err
		}
		if change.At, err = parseTime(at); err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

// maskForAssistant replaces every sensitive value of a photo's text with a
// handle of p's for it (docs/architecture/photo-ocr.md#ai-助手能用看不到具体的值):
// the sensitive fields, what was read and what members corrected, and the
// numbers textread.Numbers finds in the whole text.
func (s *Service) maskForAssistant(ctx context.Context, p Principal, record assetRecord, text PhotoText) (PhotoText, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var handleErr error
		handle := func(key, label string) string {
			named, err := s.textHandle(ctx, tx, p, record.ID, key, label)
			handleErr = errors.Join(handleErr, err)
			return named
		}
		var values []fieldValue
		for i, field := range text.Fields {
			if !field.Sensitive {
				continue
			}
			label, _ := textread.Sensitive(field.Name)
			for _, value := range []*string{&text.Fields[i].Value, &text.Fields[i].Read} {
				if *value != "" {
					values = append(values, fieldValue{field.Name, *value})
					*value = handle("field:"+field.Name, label)
				}
			}
		}
		text.Text = maskText(text.Text, values, handle)
		return handleErr
	})
	return text, err
}

// fieldValue is a value a sensitive field holds.
type fieldValue struct{ field, value string }

// maskText replaces in text each of the sensitive fields' values, and then
// each sensitive number, with what replace returns for the key it is told
// by: field:<name> for a value, number:<digest> for a number.
func maskText(text string, values []fieldValue, replace func(key, label string) string) string {
	for _, value := range values {
		label, _ := textread.Sensitive(value.field)
		text = textread.MaskValue(text, value.value, func() string { return replace("field:"+value.field, label) })
	}
	return textread.MaskNumbers(text, func(number textread.Number) string {
		digest := sha256.Sum256([]byte(number.Digits))
		return replace("number:"+hex.EncodeToString(digest[:16]), number.Label)
	})
}

// textHandle returns p's handle for one sensitive value of a photo, making
// one the first time. A handle is random, so nothing about the value can be
// learned from it, and stands for that value for p alone.
func (s *Service) textHandle(ctx context.Context, tx *sql.Tx, p Principal, assetID, key, label string) (string, error) {
	var handle string
	err := tx.QueryRowContext(ctx, "SELECT handle FROM text_handles WHERE account = ? AND asset_id = ? AND key = ?",
		p.UserID, assetID, key).Scan(&handle)
	if err == nil {
		return "⟦" + label + "·" + handle + "⟧", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for {
		handle = s.randomHex()[:6]
		result, err := tx.ExecContext(ctx, `
INSERT INTO text_handles(handle, account, asset_id, key, created_at) VALUES(?, ?, ?, ?, ?)
ON CONFLICT(handle) DO NOTHING`, handle, p.UserID, assetID, key, formatTime(s.now()))
		if err != nil {
			return "", err
		}
		if inserted, err := result.RowsAffected(); err != nil || inserted == 1 {
			return "⟦" + label + "·" + handle + "⟧", err
		}
	}
}
