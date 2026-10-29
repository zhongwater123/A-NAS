package family

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Kind is what a record holds.
type Kind string

const (
	KindPhotos      Kind = "photos"
	KindRecording   Kind = "recording"
	KindJournal     Kind = "journal"
	KindMeasurement Kind = "measurement"
	KindVaccine     Kind = "vaccine"
	KindMilestone   Kind = "milestone"
)

const (
	MaxTextRunes   = 5000
	MaxPhotos      = 200
	maxTitleRunes  = 30
	maxDetailRunes = 50
	maxRequestKey  = 128
	// PageSize is how many records or photos one page returns.
	PageSize = 30
)

// Measurement is one measuring session. At least one value is present.
type Measurement struct {
	HeightCm *float64 `json:"heightCm,omitempty"`
	WeightKg *float64 `json:"weightKg,omitempty"`
	HeadCm   *float64 `json:"headCm,omitempty"`
}

// VaccineDose is one dose given. Code names a vaccine in the schedule
// catalog, or is "custom" with Name filled in.
type VaccineDose struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Dose   int    `json:"dose"`
	Clinic string `json:"clinic"`
	Batch  string `json:"batch"`
}

// MilestoneFields is a milestone's title, or the automatic milestone the
// record annotates.
type MilestoneFields struct {
	Title   string `json:"title"`
	AutoKey string `json:"autoKey,omitempty"`
}

// Recording describes a record's audio file.
type Recording struct {
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
	FileName    string `json:"fileName"`
}

// RecordPhoto is a photo or video in a record that the reader can see.
type RecordPhoto struct {
	AssetID string     `json:"assetId"`
	TakenAt *time.Time `json:"takenAt,omitempty"`
	Video   bool       `json:"video"`
	// Role is "album" for photos that belong in the cloud album and
	// "certificate" for vaccination records' proof.
	Role string `json:"role"`
}

type Record struct {
	ID         string `json:"id"`
	BabyID     string `json:"babyId"`
	Kind       Kind   `json:"kind"`
	OccurredOn string `json:"occurredOn"`
	// Age is the baby's age on OccurredOn; nil before birth.
	Age          *Age             `json:"age,omitempty"`
	Text         string           `json:"text"`
	Photos       []RecordPhoto    `json:"photos"`
	Measurement  *Measurement     `json:"measurement,omitempty"`
	Vaccine      *VaccineDose     `json:"vaccine,omitempty"`
	Milestone    *MilestoneFields `json:"milestone,omitempty"`
	Recording    *Recording       `json:"recording,omitempty"`
	AuthorID     string           `json:"authorId"`
	AuthorName   string           `json:"authorName"`
	CreatedAt    time.Time        `json:"createdAt"`
	UpdatedAt    *time.Time       `json:"updatedAt,omitempty"`
	Version      int64            `json:"version"`
	DeletedAt    *time.Time       `json:"deletedAt,omitempty"`
	Comments     []Comment        `json:"comments"`
	HiddenPhotos int              `json:"-"`
}

// RecordInput is what a member writes. On update, Version is the version
// they read; Photos, when not nil, replaces the record's photos.
type RecordInput struct {
	RequestKey   string           `json:"requestKey"`
	Kind         Kind             `json:"kind"`
	OccurredOn   string           `json:"occurredOn"`
	Text         string           `json:"text"`
	Photos       []string         `json:"photos"`
	Certificates []string         `json:"certificates"`
	AlsoBabies   []string         `json:"alsoBabies"`
	Measurement  *Measurement     `json:"measurement,omitempty"`
	Vaccine      *VaccineDose     `json:"vaccine,omitempty"`
	Milestone    *MilestoneFields `json:"milestone,omitempty"`
	Version      int64            `json:"version"`
}

// Page is one page of records, newest first.
type Page struct {
	Records []Record `json:"records"`
	Next    string   `json:"next,omitempty"`
}

var measurementBounds = struct{ height, weight, head [2]float64 }{
	height: [2]float64{30, 200},
	weight: [2]float64{0.5, 100},
	head:   [2]float64{20, 70},
}

func validText(text string, limit int, required bool) (string, bool) {
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > limit || strings.ContainsRune(text, 0) {
		return "", false
	}
	return text, !required || text != ""
}

func inRange(value *float64, bounds [2]float64) bool {
	return value == nil || (*value >= bounds[0] && *value <= bounds[1] && !math.IsNaN(*value))
}

// row is a record's columns as stored.
type row struct {
	record            Record
	heightMM, weightG sql.NullInt64
	headMM, dose      sql.NullInt64
	code, vaccineName sql.NullString
	clinic, batch     sql.NullString
	title, autoKey    sql.NullString
	recType, recName  sql.NullString
	recBytes          sql.NullInt64
	created           string
	updated, deleted  sql.NullString
	deletedBy         string
	photoIDs          []string
	photoRoles        map[string]string
	photoTaken        map[string]*time.Time
	photoVideo        map[string]bool
}

const recordColumns = `id, baby_id, kind, occurred_on, text, author_id, author_name, created_at, updated_at, version,
deleted_at, deleted_by, height_mm, weight_g, head_mm, vaccine_code, vaccine_name, dose, clinic, batch, title, auto_key,
recording_type, recording_bytes, recording_name`

func scanRecord(scanner interface{ Scan(...any) error }) (*row, error) {
	r := &row{}
	rec := &r.record
	if err := scanner.Scan(&rec.ID, &rec.BabyID, &rec.Kind, &rec.OccurredOn, &rec.Text, &rec.AuthorID, &rec.AuthorName,
		&r.created, &r.updated, &rec.Version, &r.deleted, &r.deletedBy, &r.heightMM, &r.weightG, &r.headMM,
		&r.code, &r.vaccineName, &r.dose, &r.clinic, &r.batch, &r.title, &r.autoKey,
		&r.recType, &r.recBytes, &r.recName); err != nil {
		return nil, err
	}
	rec.CreatedAt = parseTime(r.created)
	rec.UpdatedAt = parseOptionalTime(r.updated)
	rec.DeletedAt = parseOptionalTime(r.deleted)
	switch rec.Kind {
	case KindMeasurement:
		m := &Measurement{}
		if r.heightMM.Valid {
			m.HeightCm = new(float64(r.heightMM.Int64) / 10)
		}
		if r.weightG.Valid {
			m.WeightKg = new(float64(r.weightG.Int64) / 1000)
		}
		if r.headMM.Valid {
			m.HeadCm = new(float64(r.headMM.Int64) / 10)
		}
		rec.Measurement = m
	case KindVaccine:
		rec.Vaccine = &VaccineDose{Code: r.code.String, Name: r.vaccineName.String, Dose: int(r.dose.Int64),
			Clinic: r.clinic.String, Batch: r.batch.String}
	case KindMilestone:
		rec.Milestone = &MilestoneFields{Title: r.title.String, AutoKey: r.autoKey.String}
	case KindRecording:
		rec.Recording = &Recording{ContentType: r.recType.String, SizeBytes: r.recBytes.Int64, FileName: r.recName.String}
	}
	return r, nil
}

// loadPhotos fills each row's stored photos.
func loadPhotos(ctx context.Context, q queryer, rows []*row) error {
	if len(rows) == 0 {
		return nil
	}
	byID := map[string]*row{}
	ids := make([]any, 0, len(rows))
	for _, r := range rows {
		r.photoRoles, r.photoTaken, r.photoVideo = map[string]string{}, map[string]*time.Time{}, map[string]bool{}
		byID[r.record.ID] = r
		ids = append(ids, r.record.ID)
	}
	result, err := q.QueryContext(ctx, "SELECT record_id, asset_id, role, taken_at, video FROM record_photos WHERE record_id IN ("+
		placeholders(len(ids))+") ORDER BY record_id, position", ids...)
	if err != nil {
		return err
	}
	defer func() { _ = result.Close() }()
	for result.Next() {
		var recordID, assetID, role string
		var taken sql.NullString
		var video bool
		if err := result.Scan(&recordID, &assetID, &role, &taken, &video); err != nil {
			return err
		}
		r := byID[recordID]
		r.photoIDs = append(r.photoIDs, assetID)
		r.photoRoles[assetID] = role
		r.photoTaken[assetID] = parseOptionalTime(taken)
		r.photoVideo[assetID] = video
	}
	return result.Err()
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// present turns stored rows into what actor may read: only the photos actor
// can see now, ages, and comments. A photo record whose photos are all gone
// for the reader is dropped.
func (s *Store) present(ctx context.Context, q queryer, actor Actor, baby Baby, rows []*row) ([]Record, error) {
	if err := loadPhotos(ctx, q, rows); err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.photoIDs...)
	}
	visible, known, err := s.readable(ctx, actor, ids)
	if err != nil {
		return nil, err
	}
	comments, err := s.commentsFor(ctx, q, rows)
	if err != nil {
		return nil, err
	}
	birth, _ := parseDate(baby.BirthDate)
	records := make([]Record, 0, len(rows))
	for _, r := range rows {
		rec := r.record
		rec.Photos = []RecordPhoto{}
		for _, id := range r.photoIDs {
			photo, ok := visible[id]
			if !known {
				photo, ok = Photo{ID: id, Video: r.photoVideo[id]}, true
			}
			if !ok {
				rec.HiddenPhotos++
				continue
			}
			taken := photo.TakenAt
			if taken == nil {
				taken = r.photoTaken[id]
			}
			rec.Photos = append(rec.Photos, RecordPhoto{AssetID: id, TakenAt: taken, Video: photo.Video, Role: r.photoRoles[id]})
		}
		if rec.Kind == KindPhotos && len(rec.Photos) == 0 {
			continue
		}
		if date, ok := parseDate(rec.OccurredOn); ok {
			if age, ok := ageOn(birth, date); ok {
				rec.Age = &age
			}
		}
		rec.Comments = comments[rec.ID]
		if rec.Comments == nil {
			rec.Comments = []Comment{}
		}
		records = append(records, rec)
	}
	return records, nil
}

// Records pages through a baby's records, most recently added first. kind,
// when set, keeps only that kind.
func (s *Store) Records(ctx context.Context, actor Actor, babyID string, kind Kind, cursor string) (Page, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Page{}, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return Page{}, err
	}
	after, err := parseCursor(cursor)
	if err != nil {
		return Page{}, ErrInvalid
	}
	page := Page{Records: []Record{}}
	// A page may lose photo records whose photos the reader can no longer
	// see; keep reading until it is full or the channel ends.
	for len(page.Records) < PageSize {
		query := "SELECT " + recordColumns + " FROM records WHERE baby_id = ? AND deleted_at IS NULL"
		args := []any{babyID}
		if kind != "" {
			query += " AND kind = ?"
			args = append(args, kind)
		}
		if after != nil {
			query += " AND (created_at < ? OR (created_at = ? AND id < ?))"
			args = append(args, after.at, after.at, after.id)
		}
		query += " ORDER BY created_at DESC, id DESC LIMIT ?"
		args = append(args, PageSize)
		rows, err := s.queryRows(ctx, s.db, query, args...)
		if err != nil {
			return Page{}, err
		}
		if len(rows) == 0 {
			page.Next = ""
			break
		}
		records, err := s.present(ctx, s.db, actor, baby, rows)
		if err != nil {
			return Page{}, err
		}
		page.Records = append(page.Records, records...)
		last := rows[len(rows)-1]
		after = &cursorKey{at: last.created, id: last.record.ID}
		page.Next = after.String()
		if len(rows) < PageSize {
			page.Next = ""
			break
		}
	}
	return page, nil
}

func (s *Store) queryRows(ctx context.Context, q queryer, query string, args ...any) ([]*row, error) {
	result, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = result.Close() }()
	var rows []*row
	for result.Next() {
		r, err := scanRecord(result)
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, result.Err()
}

// Record returns one record that is not in the trash.
func (s *Store) Record(ctx context.Context, actor Actor, id string) (Record, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Record{}, err
	}
	return s.readRecord(ctx, s.db, actor, id, false)
}

func (s *Store) readRecord(ctx context.Context, q queryer, actor Actor, id string, deleted bool) (Record, error) {
	condition := " AND deleted_at IS NULL"
	if deleted {
		condition = " AND deleted_at IS NOT NULL"
	}
	r, err := scanRecord(q.QueryRowContext(ctx, "SELECT "+recordColumns+" FROM records WHERE id = ?"+condition, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	baby, err := s.babyAny(ctx, q, r.record.BabyID)
	if err != nil {
		return Record{}, err
	}
	records, err := s.present(ctx, q, actor, baby, []*row{r})
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, ErrNotFound
	}
	return records[0], nil
}

// babyAny reads a baby that is not in the trash, or ErrNotFound.
func (s *Store) babyAny(ctx context.Context, q queryer, id string) (Baby, error) {
	return s.baby(ctx, q, id)
}

// checked is a validated record input with its photos looked up.
type checked struct {
	input      RecordInput
	occurredOn string
	photos     []Photo
	roles      []string
	heightMM   sql.NullInt64
	weightG    sql.NullInt64
	headMM     sql.NullInt64
}

// check validates input for baby and looks its photos up as actor. Photos
// must be visible to actor and in the shared library.
func (s *Store) check(ctx context.Context, actor Actor, baby Baby, input RecordInput) (checked, error) {
	c := checked{input: input}
	var ok bool
	limit := MaxTextRunes
	switch input.Kind {
	case KindPhotos, KindJournal, KindMeasurement, KindVaccine, KindMilestone:
	default:
		return c, ErrInvalid
	}
	if c.input.Text, ok = validText(input.Text, limit, input.Kind == KindJournal && len(input.Photos) == 0); !ok {
		return c, ErrInvalid
	}
	if len(input.RequestKey) > maxRequestKey || !utf8.ValidString(input.RequestKey) {
		return c, ErrInvalid
	}
	birth, _ := parseDate(baby.BirthDate)
	today := s.today()
	ids := slices.Clone(input.Photos)
	roles := slices.Repeat([]string{"album"}, len(ids))
	switch input.Kind {
	case KindPhotos:
		if len(ids) == 0 || input.Measurement != nil || input.Vaccine != nil || input.Milestone != nil {
			return c, ErrInvalid
		}
	case KindJournal:
		if input.Measurement != nil || input.Vaccine != nil || input.Milestone != nil {
			return c, ErrInvalid
		}
	case KindMeasurement:
		m := input.Measurement
		if m == nil || (m.HeightCm == nil && m.WeightKg == nil && m.HeadCm == nil) || len(ids) > 0 ||
			!inRange(m.HeightCm, measurementBounds.height) || !inRange(m.WeightKg, measurementBounds.weight) ||
			!inRange(m.HeadCm, measurementBounds.head) {
			return c, ErrInvalid
		}
		c.heightMM = roundTo(m.HeightCm, 10)
		c.weightG = roundTo(m.WeightKg, 1000)
		c.headMM = roundTo(m.HeadCm, 10)
	case KindVaccine:
		v := input.Vaccine
		if v == nil || len(ids) > 0 || v.Dose < 1 || v.Dose > 10 {
			return c, ErrInvalid
		}
		if v.Code == CustomVaccine {
			if v.Name, ok = validText(v.Name, maxTitleRunes, true); !ok {
				return c, ErrInvalid
			}
		} else if _, known := vaccineByCode(v.Code); !known {
			return c, ErrInvalid
		} else {
			v.Name = ""
		}
		if v.Clinic, ok = validText(v.Clinic, maxDetailRunes, false); !ok {
			return c, ErrInvalid
		}
		if v.Batch, ok = validText(v.Batch, maxDetailRunes, false); !ok {
			return c, ErrInvalid
		}
		c.input.Vaccine = v
		ids = slices.Clone(input.Certificates)
		roles = slices.Repeat([]string{"certificate"}, len(ids))
	case KindMilestone:
		m := input.Milestone
		if m == nil {
			return c, ErrInvalid
		}
		if m.AutoKey != "" {
			auto, found := autoMilestone(birth, m.AutoKey)
			if !found || auto.Date.After(today) {
				return c, ErrInvalid
			}
			m.Title = auto.Title
			c.input.OccurredOn = auto.Date.Format(dateLayout)
		} else if m.Title, ok = validText(m.Title, maxTitleRunes, true); !ok {
			return c, ErrInvalid
		}
		c.input.Milestone = m
	}
	if len(ids) > MaxPhotos {
		return c, ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return c, ErrInvalid
		}
		seen[id] = true
	}
	found, err := s.lookup(ctx, actor, ids)
	if err != nil {
		return c, err
	}
	for _, id := range ids {
		photo, ok := found[id]
		if !ok || !photo.Shared {
			return c, ErrInvalid
		}
		c.photos = append(c.photos, photo)
	}
	c.roles = roles
	// Dates: photo records date from their earliest photo, others from what
	// the member chose. Nothing happens after today; measurements, doses and
	// milestones happen in the baby's life, while photos and journals may
	// come from before birth.
	date := c.input.OccurredOn
	if input.Kind == KindPhotos {
		date = ""
		for _, photo := range c.photos {
			if photo.TakenAt != nil {
				local := photo.TakenAt.In(time.Local)
				day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Format(dateLayout)
				if date == "" || day < date {
					date = day
				}
			}
		}
		if date == "" {
			date = today.Format(dateLayout)
		}
	}
	day, ok := parseDate(date)
	if !ok || day.After(today) {
		return c, ErrInvalid
	}
	if day.Before(birth) && (input.Kind == KindMeasurement || input.Kind == KindVaccine || input.Kind == KindMilestone) {
		return c, ErrInvalid
	}
	c.occurredOn = date
	return c, nil
}

func roundTo(value *float64, scale float64) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(math.Round(*value * scale)), Valid: true}
}

// AddRecord adds a record to a baby's channel. A photo record may also go
// into the channels of other babies, as a record of its own in each. A
// request repeated with the same RequestKey returns the first record.
func (s *Store) AddRecord(ctx context.Context, actor Actor, babyID string, input RecordInput) (Record, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Record{}, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return Record{}, err
	}
	if input.RequestKey != "" {
		if existing, err := s.byRequest(ctx, actor, input.RequestKey); err == nil {
			return existing, nil
		}
	}
	c, err := s.check(ctx, actor, baby, input)
	if err != nil {
		return Record{}, err
	}
	if len(input.AlsoBabies) > 0 && input.Kind != KindPhotos {
		return Record{}, ErrInvalid
	}
	var id string
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if _, _, err := s.requireMember(ctx, tx, actor); err != nil {
			return err
		}
		targets := []string{babyID}
		for _, other := range input.AlsoBabies {
			if other == babyID || slices.Contains(targets, other) {
				continue
			}
			if _, err := s.baby(ctx, tx, other); err != nil {
				return ErrInvalid
			}
			targets = append(targets, other)
		}
		for i, target := range targets {
			key := sql.NullString{String: input.RequestKey, Valid: input.RequestKey != "" && i == 0}
			recordID, err := s.insertRecord(ctx, tx, actor, target, c, key)
			if err != nil {
				return err
			}
			if i == 0 {
				id = recordID
			}
		}
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return s.readRecord(ctx, s.db, actor, id, false)
}

func (s *Store) byRequest(ctx context.Context, actor Actor, key string) (Record, error) {
	var id string
	err := s.db.QueryRowContext(ctx, "SELECT id FROM records WHERE author_id = ? AND request_key = ?", actor.AccountID, key).Scan(&id)
	if err != nil {
		return Record{}, ErrNotFound
	}
	return s.readRecord(ctx, s.db, actor, id, false)
}

func (s *Store) insertRecord(ctx context.Context, tx *sql.Tx, actor Actor, babyID string, c checked, key sql.NullString) (string, error) {
	id := newID("rec")
	input := c.input
	var code, vaccineName, clinic, batch, title, autoKey sql.NullString
	var dose sql.NullInt64
	if v := input.Vaccine; v != nil {
		code, vaccineName = nullString(v.Code), nullString(v.Name)
		clinic, batch = nullString(v.Clinic), nullString(v.Batch)
		dose = sql.NullInt64{Int64: int64(v.Dose), Valid: true}
	}
	if m := input.Milestone; m != nil {
		title, autoKey = nullString(m.Title), nullString(m.AutoKey)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO records(id, baby_id, kind, occurred_on, text, author_id, author_name, created_at,
request_key, height_mm, weight_g, head_mm, vaccine_code, vaccine_name, dose, clinic, batch, title, auto_key)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, babyID, input.Kind, c.occurredOn, input.Text, actor.AccountID, actor.Username, formatTime(s.now()),
		key, c.heightMM, c.weightG, c.headMM, code, vaccineName, dose, clinic, batch, title, autoKey)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", ErrConflict
		}
		return "", err
	}
	if err := insertPhotos(ctx, tx, id, c); err != nil {
		return "", err
	}
	return id, s.audit(ctx, tx, actor, "family.record_added", id, string(input.Kind))
}

func insertPhotos(ctx context.Context, tx *sql.Tx, recordID string, c checked) error {
	for i, photo := range c.photos {
		var taken sql.NullString
		if photo.TakenAt != nil {
			taken = sql.NullString{String: formatTime(*photo.TakenAt), Valid: true}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO record_photos(record_id, asset_id, position, role, taken_at, video) VALUES(?, ?, ?, ?, ?, ?)",
			recordID, photo.ID, i, c.roles[i], taken, photo.Video); err != nil {
			return err
		}
	}
	return nil
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

// UpdateRecord changes the caller's own record. Kind stays; for a recording
// only the text and date change.
func (s *Store) UpdateRecord(ctx context.Context, actor Actor, id string, input RecordInput) (Record, error) {
	if _, _, err := s.requireMember(ctx, s.db, actor); err != nil {
		return Record{}, err
	}
	current, err := s.scanStored(ctx, s.db, id)
	if err != nil {
		return Record{}, err
	}
	if current.record.AuthorID != actor.AccountID {
		return Record{}, ErrForbidden
	}
	baby, err := s.baby(ctx, s.db, current.record.BabyID)
	if err != nil {
		return Record{}, err
	}
	input.Kind = current.record.Kind
	input.AlsoBabies = nil
	recording := input.Kind == KindRecording
	var c checked
	if recording {
		text, ok := validText(input.Text, maxRecordingTitleRunes, false)
		date, dateOK := parseDate(input.OccurredOn)
		if !ok || !dateOK || date.After(s.today()) {
			return Record{}, ErrInvalid
		}
		c = checked{input: RecordInput{Kind: KindRecording, Text: text}, occurredOn: input.OccurredOn}
	} else {
		if input.Photos == nil && input.Certificates == nil {
			if err := loadPhotos(ctx, s.db, []*row{current}); err != nil {
				return Record{}, err
			}
			for _, assetID := range current.photoIDs {
				if current.photoRoles[assetID] == "certificate" {
					input.Certificates = append(input.Certificates, assetID)
				} else {
					input.Photos = append(input.Photos, assetID)
				}
			}
		}
		if c, err = s.check(ctx, actor, baby, input); err != nil {
			return Record{}, err
		}
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var version int64
		var authorID string
		err := tx.QueryRowContext(ctx, "SELECT version, author_id FROM records WHERE id = ? AND deleted_at IS NULL", id).
			Scan(&version, &authorID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.Version {
			return ErrChanged
		}
		in := c.input
		var code, vaccineName, clinic, batch, title, autoKey sql.NullString
		var dose sql.NullInt64
		if v := in.Vaccine; v != nil {
			code, vaccineName = nullString(v.Code), nullString(v.Name)
			clinic, batch = nullString(v.Clinic), nullString(v.Batch)
			dose = sql.NullInt64{Int64: int64(v.Dose), Valid: true}
		}
		if m := in.Milestone; m != nil {
			title, autoKey = nullString(m.Title), nullString(m.AutoKey)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE records SET occurred_on = ?, text = ?, updated_at = ?, version = version + 1,
height_mm = ?, weight_g = ?, head_mm = ?, vaccine_code = ?, vaccine_name = ?, dose = ?, clinic = ?, batch = ?, title = ?, auto_key = ?
WHERE id = ?`, c.occurredOn, in.Text, formatTime(s.now()), c.heightMM, c.weightG, c.headMM,
			code, vaccineName, dose, clinic, batch, title, autoKey, id); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return err
		}
		if !recording {
			if _, err := tx.ExecContext(ctx, "DELETE FROM record_photos WHERE record_id = ?", id); err != nil {
				return err
			}
			if err := insertPhotos(ctx, tx, id, c); err != nil {
				return err
			}
		}
		return s.audit(ctx, tx, actor, "family.record_updated", id, string(current.record.Kind))
	})
	if err != nil {
		return Record{}, err
	}
	return s.readRecord(ctx, s.db, actor, id, false)
}

// scanStored reads a record row that is not in the trash, without photos.
func (s *Store) scanStored(ctx context.Context, q queryer, id string) (*row, error) {
	r, err := scanRecord(q.QueryRowContext(ctx, "SELECT "+recordColumns+" FROM records WHERE id = ? AND deleted_at IS NULL", id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// DeleteRecord moves a record into the channel's recent deletions. Authors
// delete their own records; guardians delete anyone's.
func (s *Store) DeleteRecord(ctx context.Context, actor Actor, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, me, err := s.requireMember(ctx, tx, actor)
		if err != nil {
			return err
		}
		current, err := s.scanStored(ctx, tx, id)
		if err != nil {
			return err
		}
		if current.record.AuthorID != actor.AccountID && !me.Guardian {
			return ErrForbidden
		}
		if _, err := tx.ExecContext(ctx, "UPDATE records SET deleted_at = ?, deleted_by = ? WHERE id = ?",
			formatTime(s.now()), actor.AccountID, id); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "family.record_deleted", id, string(current.record.Kind))
	})
}

// RestoreRecord brings a deleted record back with its comments.
func (s *Store) RestoreRecord(ctx context.Context, actor Actor, id string) (Record, error) {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, me, err := s.requireMember(ctx, tx, actor)
		if err != nil {
			return err
		}
		r, err := scanRecord(tx.QueryRowContext(ctx, "SELECT "+recordColumns+
			" FROM records WHERE id = ? AND deleted_at IS NOT NULL AND deleted_at > ?", id, s.trashCutoff()))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if r.record.AuthorID != actor.AccountID && !me.Guardian {
			return ErrForbidden
		}
		if _, err := s.baby(ctx, tx, r.record.BabyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE records SET deleted_at = NULL, deleted_by = '' WHERE id = ?", id); err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				return ErrConflict
			}
			return err
		}
		return s.audit(ctx, tx, actor, "family.record_restored", id, string(r.record.Kind))
	})
	if err != nil {
		return Record{}, err
	}
	return s.readRecord(ctx, s.db, actor, id, false)
}

// Trash lists a baby's recently deleted records that the caller may
// restore: their own, or everyone's for a guardian.
func (s *Store) Trash(ctx context.Context, actor Actor, babyID string) ([]Record, error) {
	_, me, err := s.requireMember(ctx, s.db, actor)
	if err != nil {
		return nil, err
	}
	baby, err := s.baby(ctx, s.db, babyID)
	if err != nil {
		return nil, err
	}
	query := "SELECT " + recordColumns + " FROM records WHERE baby_id = ? AND deleted_at IS NOT NULL AND deleted_at > ?"
	args := []any{babyID, s.trashCutoff()}
	if !me.Guardian {
		query += " AND author_id = ?"
		args = append(args, actor.AccountID)
	}
	rows, err := s.queryRows(ctx, s.db, query+" ORDER BY deleted_at DESC LIMIT 500", args...)
	if err != nil {
		return nil, err
	}
	records, err := s.present(ctx, s.db, actor, baby, rows)
	if err != nil {
		return nil, err
	}
	return records, nil
}
