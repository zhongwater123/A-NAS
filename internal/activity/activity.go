// Package activity reads the audit trails that A-NAS modules keep in their own
// SQLite databases as one activity log: every module's audit_events table has
// the same columns, so a single query builder pages each of them by the same
// cursor and Merge combines the pages newest first.
package activity

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Category groups actions for the activity log's filters.
type Category string

const (
	CategorySession Category = "session"
	CategoryAccount Category = "account"
	CategoryFile    Category = "file"
	CategoryStorage Category = "storage"
	CategoryPhoto   Category = "photo"
	CategoryOther   Category = "other"
)

// Categories lists the categories a client may filter by.
var Categories = []Category{CategorySession, CategoryAccount, CategoryFile, CategoryStorage, CategoryPhoto}

// categoryPrefixes maps each category to the action prefixes that belong to
// it. "photos.viewing" (an account action) must not match "photo.".
var categoryPrefixes = map[Category][]string{
	CategorySession: {"session."},
	CategoryAccount: {"user.", "space.viewing", "photos.viewing"},
	CategoryFile:    {"file.", "snapshot."},
	CategoryStorage: {"storage."},
	CategoryPhoto:   {"photo."},
}

// CategoryOf returns the category of an action.
func CategoryOf(action string) Category {
	for _, category := range Categories {
		for _, prefix := range categoryPrefixes[category] {
			if strings.HasPrefix(action, prefix) {
				return category
			}
		}
	}
	return CategoryOther
}

// ValidCategory reports whether a client-supplied category is known.
func ValidCategory(category Category) bool {
	_, ok := categoryPrefixes[category]
	return ok
}

// Event is one audit record. Key is its position in the log as the source
// database orders it; pages continue from the Key of their last event.
type Event struct {
	ID           string
	ActorUserID  string
	Action       string
	ResourceType string
	ResourceID   string
	OccurredAt   time.Time
	Detail       string
	// SpaceID is the space a file event happened in, when the source knows it.
	SpaceID string
	Key     Cursor
}

// Cursor orders events by time, then ID. At is UTC with milliseconds in the
// fixed-width form SQLite's strftime('%Y-%m-%dT%H:%M:%f') prints, so it sorts
// as text; the stored RFC 3339 strings drop trailing zeros and do not.
type Cursor struct {
	At string
	ID string
}

const cursorLayout = "2006-01-02T15:04:05.000"

var cursorAt = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}$`)

var ErrInvalidCursor = errors.New("activity cursor is invalid")

// String encodes the cursor for clients, which treat it as opaque.
func (c Cursor) String() string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.At + "|" + c.ID))
}

// ParseCursor decodes a cursor returned by String.
func ParseCursor(value string) (Cursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	at, id, ok := strings.Cut(string(decoded), "|")
	if !ok || !cursorAt.MatchString(at) || id == "" || len(id) > 200 {
		return Cursor{}, ErrInvalidCursor
	}
	return Cursor{At: at, ID: id}, nil
}

// Less reports whether c comes before other in the log, which is newest first.
func (c Cursor) Less(other Cursor) bool {
	if c.At != other.At {
		return c.At > other.At
	}
	return c.ID > other.ID
}

// TimeBound formats a time as a bound comparable with Cursor.At.
func TimeBound(value time.Time) string { return value.UTC().Format(cursorLayout) }

// Subject limits events to those done by any of ActorIDs or done to any of
// ResourceIDs.
type Subject struct {
	ActorIDs    []string
	ResourceIDs []string
}

// Reveal says which events' names the viewer may see: their own, and those in
// SpaceIDs. Text search never matches the names of other events.
type Reveal struct {
	ViewerID string
	SpaceIDs []string
}

// Revealed reports whether the viewer may see the names in event.
func (r *Reveal) Revealed(event Event) bool {
	if r == nil || event.ActorUserID == r.ViewerID {
		return true
	}
	for _, id := range r.SpaceIDs {
		if id != "" && event.SpaceID == id {
			return true
		}
	}
	return false
}

// Query selects one page of events.
type Query struct {
	Before     *Cursor
	Since      time.Time
	Until      time.Time
	Categories []Category
	// Subject nil means everyone's events.
	Subject *Subject
	// Text matches the action, resource ID and detail, case-insensitively, and
	// TextActors adds the events done by accounts whose name matched.
	Text       string
	TextActors []string
	// Reveal nil means every name may be searched.
	Reveal *Reveal
	Limit  int
}

// Wants reports whether a source holding only the given categories can have
// any event for q.
func (q Query) Wants(categories ...Category) bool {
	if len(q.Categories) == 0 {
		return true
	}
	for _, wanted := range q.Categories {
		for _, held := range categories {
			if wanted == held {
				return true
			}
		}
	}
	return false
}

// Page sizes and search length accepted from clients.
const (
	DefaultLimit = 50
	MaxLimit     = 200
	MaxTextRunes = 100
)

var ErrInvalidQuery = errors.New("activity query is invalid")

// ParseQuery reads the filters a client sent: before (a cursor), since and
// until (RFC 3339), category, q and limit. Who the query is about is up to
// the caller.
func ParseQuery(values url.Values) (Query, error) {
	q := Query{Limit: DefaultLimit, Text: strings.TrimSpace(values.Get("q"))}
	if utf8.RuneCountInString(q.Text) > MaxTextRunes {
		return Query{}, ErrInvalidQuery
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > MaxLimit {
			return Query{}, ErrInvalidQuery
		}
		q.Limit = limit
	}
	if raw := values.Get("before"); raw != "" {
		cursor, err := ParseCursor(raw)
		if err != nil {
			return Query{}, ErrInvalidQuery
		}
		q.Before = &cursor
	}
	for name, target := range map[string]*time.Time{"since": &q.Since, "until": &q.Until} {
		if raw := values.Get(name); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return Query{}, ErrInvalidQuery
			}
			*target = parsed
		}
	}
	if raw := values.Get("category"); raw != "" {
		if !ValidCategory(Category(raw)) {
			return Query{}, ErrInvalidQuery
		}
		q.Categories = []Category{Category(raw)}
	}
	return q, nil
}

// Values encodes the parts of q that ParseQuery reads.
func (q Query) Values() url.Values {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(q.Limit))
	if q.Text != "" {
		values.Set("q", q.Text)
	}
	if q.Before != nil {
		values.Set("before", q.Before.String())
	}
	if !q.Since.IsZero() {
		values.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		values.Set("until", q.Until.UTC().Format(time.RFC3339))
	}
	if len(q.Categories) == 1 {
		values.Set("category", string(q.Categories[0]))
	}
	return values
}

// Table reads one module's audit_events table.
type Table struct {
	DB *sql.DB
	// SpaceColumn says the table records the space of each event.
	SpaceColumn bool
}

// List returns up to q.Limit+1 events matching q, newest first, so callers
// can tell whether more follow.
func (t Table) List(ctx context.Context, q Query) ([]Event, error) {
	space := "''"
	if t.SpaceColumn {
		space = "space_id"
	}
	statement := `SELECT id, actor, action, resource_type, resource_id, occurred_at, detail, space, at FROM (
SELECT id, COALESCE(actor_user_id, '') AS actor, action, resource_type, resource_id, occurred_at, detail, ` + space + ` AS space,
COALESCE(strftime('%Y-%m-%dT%H:%M:%f', occurred_at), '0000-00-00T00:00:00.000') AS at
FROM audit_events)`
	var conditions []string
	var arguments []any
	add := func(condition string, values ...any) {
		conditions = append(conditions, condition)
		arguments = append(arguments, values...)
	}
	if q.Before != nil {
		add("(at < ? OR (at = ? AND id < ?))", q.Before.At, q.Before.At, q.Before.ID)
	}
	if !q.Since.IsZero() {
		add("at >= ?", TimeBound(q.Since))
	}
	if !q.Until.IsZero() {
		add("at < ?", TimeBound(q.Until))
	}
	if len(q.Categories) > 0 {
		var alternatives []string
		for _, category := range q.Categories {
			for _, prefix := range categoryPrefixes[category] {
				alternatives = append(alternatives, "substr(action, 1, ?) = ?")
				arguments = append(arguments, len(prefix), prefix)
			}
		}
		if len(alternatives) == 0 {
			return nil, nil
		}
		conditions = append(conditions, "("+strings.Join(alternatives, " OR ")+")")
	}
	if q.Subject != nil {
		var alternatives []string
		if len(q.Subject.ActorIDs) > 0 {
			alternatives = append(alternatives, "actor IN ("+placeholders(len(q.Subject.ActorIDs))+")")
			arguments = append(arguments, values(q.Subject.ActorIDs)...)
		}
		if len(q.Subject.ResourceIDs) > 0 {
			alternatives = append(alternatives, "resource_id IN ("+placeholders(len(q.Subject.ResourceIDs))+")")
			arguments = append(arguments, values(q.Subject.ResourceIDs)...)
		}
		if len(alternatives) == 0 {
			return nil, nil
		}
		conditions = append(conditions, "("+strings.Join(alternatives, " OR ")+")")
	}
	if text := strings.ToLower(strings.TrimSpace(q.Text)); text != "" {
		names := "instr(lower(resource_id), ?) > 0 OR instr(lower(detail), ?) > 0"
		nameArguments := []any{text, text}
		if q.Reveal != nil {
			// Names outside the viewer's reach must not be found by guessing.
			allowed := []string{"actor = ?"}
			allowedArguments := []any{q.Reveal.ViewerID}
			if len(q.Reveal.SpaceIDs) > 0 {
				allowed = append(allowed, "space IN ("+placeholders(len(q.Reveal.SpaceIDs))+")")
				allowedArguments = append(allowedArguments, values(q.Reveal.SpaceIDs)...)
			}
			names = "((" + strings.Join(allowed, " OR ") + ") AND (" + names + "))"
			nameArguments = append(allowedArguments, nameArguments...)
		}
		alternatives := []string{"instr(lower(action), ?) > 0", names}
		arguments = append(arguments, text)
		arguments = append(arguments, nameArguments...)
		if len(q.TextActors) > 0 {
			alternatives = append(alternatives, "actor IN ("+placeholders(len(q.TextActors))+")")
			arguments = append(arguments, values(q.TextActors)...)
		}
		conditions = append(conditions, "("+strings.Join(alternatives, " OR ")+")")
	}
	if len(conditions) > 0 {
		statement += "\nWHERE " + strings.Join(conditions, " AND ")
	}
	statement += "\nORDER BY at DESC, id DESC LIMIT ?"
	arguments = append(arguments, q.Limit+1)
	rows, err := t.DB.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		var occurredAt string
		if err := rows.Scan(&event.ID, &event.ActorUserID, &event.Action, &event.ResourceType, &event.ResourceID,
			&occurredAt, &event.Detail, &event.SpaceID, &event.Key.At); err != nil {
			return nil, err
		}
		event.Key.ID = event.ID
		event.OccurredAt, _ = time.Parse(time.RFC3339Nano, occurredAt)
		events = append(events, event)
	}
	return events, rows.Err()
}

// Merge combines pages that sources returned for the same query into one page
// of at most limit events, newest first. Each page must hold the source's
// newest limit+1 matching events; more reports that older events follow.
func Merge(limit int, pages ...[]Event) (events []Event, more bool) {
	for _, page := range pages {
		events = append(events, page...)
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Key.Less(events[j].Key) })
	if len(events) > limit {
		return events[:limit], true
	}
	return events, false
}

func placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func values(items []string) []any {
	result := make([]any, len(items))
	for index, item := range items {
		result[index] = item
	}
	return result
}
