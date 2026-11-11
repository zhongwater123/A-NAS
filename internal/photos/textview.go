package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strconv"
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

// ErrSensitivePhoto refuses an assistant the view of a photo read as a
// document (an ID card, a bank card, a bank slip, an invoice, a receipt) or
// whose text holds a sensitive value.
var ErrSensitivePhoto = errors.New("the photo is a document or shows a sensitive value")

// ErrUnreadPhoto refuses an assistant the view of a photo that waits to be
// read, or whose reading failed: what it shows is not known yet.
var ErrUnreadPhoto = errors.New("the photo's text is not read yet")

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
	// Passed says the value needs no one to look at it: the reading passed
	// every check, or a member corrected it. Reasons say why not.
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons,omitempty"`
	// Sensitive values reach a member's AI assistant only as handles.
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
			// A member set the value: nothing is left to check.
			field.Value, field.Passed, field.Reasons = corrected.value, true, nil
			field.Correction = &TextCorrection{UserID: corrected.account, Username: corrected.username, At: corrected.at}
		}
		text.Fields = append(text.Fields, field)
	}
	if p.Assistant {
		return s.maskForAssistant(ctx, p, record, text)
	}
	return text, nil
}

// assistantMayLook says whether an assistant gets the view of an object, by
// what was read from it (docs/decisions/2026-11-10-assistant-view-of-documents.md).
// The view goes to the assistant's model, in the cloud for now, so the photo
// service refuses it whenever the picture may show what the assistant knows
// only as a handle, or not at all:
//
//   - ErrSensitivePhoto for a photo read as a document, whichever side and
//     whatever its fields hold: the back of an ID card has no number and is an
//     ID card all the same. Also for any other photo whose text holds a number
//     by textread.Numbers' rules, except a mobile number, which a screenshot
//     often shows and the user lets an assistant look at (2026-11-06).
//   - ErrUnreadPhoto for a photo routed to be read and not read yet, or whose
//     reading failed: until it is read, nobody knows what it shows. Reading a
//     photo takes minutes, and an ID card just added would be handed out whole
//     meanwhile.
//
// A photo the route does not send to be read, and one in libraries with
// reading off, has no reading and no job: the assistant may look.
func (s *Service) assistantMayLook(ctx context.Context, objectID string) error {
	reading, found, err := s.storedReading(ctx, s.db, objectID)
	if err != nil {
		return err
	}
	if !found {
		status, err := s.textStatus(ctx, objectID)
		if err != nil || status == TextNone {
			return err
		}
		return ErrUnreadPhoto
	}
	if textread.Document(textread.Kind(reading.kind)) {
		return ErrSensitivePhoto
	}
	if slices.ContainsFunc(textread.Numbers(reading.text), func(number textread.Number) bool { return number.Label != "手机号" }) {
		return ErrSensitivePhoto
	}
	return nil
}

// textState says where reading an asset's text stands, for its details, so
// that the viewer asks for the text only of a photo that has one: TextRead,
// TextQueued or TextFailed, or empty when the asset's library does not read
// text or the photo is not one to read.
func (s *Service) textState(ctx context.Context, record assetRecord) (string, error) {
	on, err := textsOn(ctx, s.db, record.library.id)
	if err != nil || !on {
		return "", err
	}
	var read bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM texts WHERE object_id = ?)",
		record.objectID).Scan(&read); err != nil {
		return "", err
	}
	if read {
		return TextRead, nil
	}
	state, err := s.textStatus(ctx, record.objectID)
	if err != nil || state == TextNone {
		return "", err
	}
	return state, nil
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
// what the sensitive fields hold and what was read for them, and the numbers
// textread.Numbers finds in the whole text and in every other field.
func (s *Service) maskForAssistant(ctx context.Context, p Principal, record assetRecord, text PhotoText) (PhotoText, error) {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		var handleErr error
		maskText(&text, func(key, label string) string {
			named, err := s.textHandle(ctx, tx, p, record.ID, key, label)
			handleErr = errors.Join(handleErr, err)
			return named
		})
		return handleErr
	})
	return text, err
}

// maskText replaces every sensitive value of text, in place, with what
// replace returns for its key, and returns the masker, which then knows the
// value each key stood for.
func maskText(text *PhotoText, replace func(key, label string) string) *textMasker {
	masker := newTextMasker(text.Fields, replace)
	text.Text = masker.mask(text.Text)
	for i, field := range text.Fields {
		masked := &text.Fields[i]
		if !field.Sensitive {
			masked.Value, masked.Read = masker.mask(field.Value), masker.mask(field.Read)
			continue
		}
		label, _ := textread.Sensitive(field.Name)
		if field.Value != "" {
			masked.Value = masker.replace(fieldKey(field.Name), label)
		}
		if field.Read != "" {
			masked.Read = masker.replace(readKey(field), label)
		}
	}
	return masker
}

// TextHandleValue is what a handle stands for, for the member whose
// assistant got it.
type TextHandleValue struct {
	// Label names the kind of value, as the handle does: 身份证号, 卡号.
	Label string `json:"label"`
	Value string `json:"value"`
	// Unchecked says the value is a field's that passed not every check and
	// that no member confirmed or corrected: machine reading may have got it
	// wrong.
	Unchecked bool `json:"unchecked"`
	// AssetID and AssetName are the photo the value was read from.
	AssetID   string `json:"assetId"`
	AssetName string `json:"assetName"`
}

var textHandlePattern = regexp.MustCompile(`^[0-9a-f]{6}$`)

// TextHandle returns the value a handle of p's assistant stands for
// (docs/architecture/photo-ocr.md#ai-助手能用看不到具体的值). Only the
// member gets it, never an assistant: the window the assistant answers in
// asks with the member's own session and shows the value in place of the
// handle, so the value reaches the member without passing the model.
//
// The handle must be p's own, and p must still see the photo, in a library
// that reads text. The value is what the handle's key names now: what the
// field holds, what was read for it, or the photo's nth number. Anything else
// is not found, and says no more than that.
func (s *Service) TextHandle(ctx context.Context, p Principal, handle string) (TextHandleValue, error) {
	if p.Assistant || !textHandlePattern.MatchString(handle) {
		return TextHandleValue{}, ErrNotFound
	}
	var assetID, key string
	err := s.db.QueryRowContext(ctx, "SELECT asset_id, key FROM text_handles WHERE handle = ? AND account = ?",
		handle, p.UserID).Scan(&assetID, &key)
	if errors.Is(err, sql.ErrNoRows) {
		return TextHandleValue{}, ErrNotFound
	}
	if err != nil {
		return TextHandleValue{}, err
	}
	record, err := s.visibleAsset(ctx, s.db, p, assetID)
	if err != nil {
		return TextHandleValue{}, err
	}
	text, err := s.photoText(ctx, p, record)
	if err != nil {
		return TextHandleValue{}, err
	}
	if text.Status != TextRead {
		return TextHandleValue{}, ErrNotFound
	}
	unchecked := false
	if name, isField := strings.CutPrefix(key, "field:"); isField {
		unchecked = slices.ContainsFunc(text.Fields, func(field TextField) bool { return field.Name == name && !field.Passed })
	}
	// The same pass that made the handles, so that a number's order is the
	// one its key was given by. What replaces a value keeps its neighbours
	// apart as a handle does: digits left side by side would read as a number
	// the assistant's pass never found.
	masker := maskText(&text, func(string, string) string { return "⟦⟧" })
	label, value, ok := masker.valueOf(key)
	if !ok {
		return TextHandleValue{}, ErrNotFound
	}
	return TextHandleValue{Label: label, Value: value, Unchecked: unchecked, AssetID: record.ID, AssetName: record.Name}, nil
}

// fieldKey keys the handle for what a sensitive field holds, and readKey
// the one for what was read for it: the field's when nobody corrected it to
// something else.
func fieldKey(name string) string { return "field:" + name }

func readKey(field TextField) string {
	if field.Read != field.Value {
		return "read:" + field.Name
	}
	return fieldKey(field.Name)
}

// sensitiveValue is a value to mask, with the key of its handle.
type sensitiveValue struct{ key, label, value string }

// textMasker replaces the sensitive values of a photo's text with what
// replace returns for their keys: field:<name> for what a sensitive field
// holds, read:<name> for what was read for it when a member corrected it to
// something else, and number:<n> for the nth sensitive number it found. A
// number is keyed by its order, not by a digest, so that nothing kept beside
// a handle tells the number.
type textMasker struct {
	values []sensitiveValue
	// numbers gives each number found its order, from 1; found lists them in
	// that order.
	numbers map[string]int
	found   []textread.Number
	replace func(key, label string) string
}

// valueOf is the value a key stood for in what the masker has masked: a
// field's, or the nth number as its digits.
func (m *textMasker) valueOf(key string) (label, value string, ok bool) {
	if order, isNumber := strings.CutPrefix(key, "number:"); isNumber {
		n, err := strconv.Atoi(order)
		if err != nil || n < 1 || n > len(m.found) {
			return "", "", false
		}
		return m.found[n-1].Label, m.found[n-1].Digits, true
	}
	for _, known := range m.values {
		if known.key == key && known.value != "" {
			return known.label, known.value, true
		}
	}
	return "", "", false
}

// newTextMasker masks the values of fields' sensitive ones.
func newTextMasker(fields []TextField, replace func(key, label string) string) *textMasker {
	masker := &textMasker{numbers: map[string]int{}, replace: replace}
	for _, field := range fields {
		if label, sensitive := textread.Sensitive(field.Name); sensitive {
			masker.values = append(masker.values,
				sensitiveValue{fieldKey(field.Name), label, field.Value}, sensitiveValue{readKey(field), label, field.Read})
		}
	}
	// A longer value first, so that one holding another is masked whole.
	slices.SortStableFunc(masker.values, func(a, b sensitiveValue) int { return len(b.value) - len(a.value) })
	return masker
}

// mask replaces in text each sensitive value, and then each sensitive number.
func (m *textMasker) mask(text string) string {
	for _, value := range m.values {
		text = textread.MaskValue(text, value.value, func() string { return m.replace(value.key, value.label) })
	}
	return textread.MaskNumbers(text, func(number textread.Number) string {
		order, ok := m.numbers[number.Digits]
		if !ok {
			order = len(m.numbers) + 1
			m.numbers[number.Digits] = order
			m.found = append(m.found, number)
		}
		return m.replace("number:"+strconv.Itoa(order), number.Label)
	})
}

// textHandle returns p's handle for one sensitive value of a photo, making
// one the first time. A handle is random, so nothing about the value can be
// learned from it, and stands for p alone for what its key names: what a
// field of the photo holds, what was read for it, or the photo's nth number.
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
