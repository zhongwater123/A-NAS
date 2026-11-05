package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// textHits are the photos whose text holds a search's query: the content
// objects found, seen in the libraries of the search that read text.
type textHits struct {
	objects, libraries map[string]bool
}

func (t textHits) holds(c candidate) bool { return t.libraries[c.libraryID] && t.objects[c.objectID] }

// textMatches finds the content objects whose whole text, field values or
// corrections hold query, among the photos listed to p in the libraries of
// scope that read text (docs/architecture/photo-ocr.md#搜索). It compares
// regardless of spaces, width and case, and a query mostly of digits also
// regardless of hyphens, so that "6222 0200" finds 6222-0200-…. An
// assistant matches only the text as it sees it, every sensitive value
// masked: searching for parts of a number would otherwise tell it the number
// a digit at a time.
func (s *Service) textMatches(ctx context.Context, p Principal, query string, scope []any) (textHits, error) {
	hits := textHits{objects: map[string]bool{}, libraries: map[string]bool{}}
	readers, err := s.strings(ctx, "SELECT id FROM libraries WHERE texts = 1 AND id IN ("+sqlitedb.Placeholders(len(scope))+")",
		scope...)
	if err != nil || len(readers) == 0 {
		return hits, err
	}
	for _, id := range readers {
		hits.libraries[id] = true
	}
	fold := textread.Fold
	if textread.Numeric(query) {
		fold = textread.FoldNumber
	}
	needle := fold(query)
	if needle == "" {
		return hits, nil
	}
	listed := make([]any, len(readers))
	for i, id := range readers {
		listed[i] = id
	}
	objects := "SELECT a.object_id FROM assets a WHERE " + listedAssets(p, "a") +
		" AND a.library_id IN (" + sqlitedb.Placeholders(len(listed)) + ")"
	haystacks := map[string][]string{}
	sensitive := map[string][]fieldValue{}
	err = s.eachRow(ctx, "SELECT object_id, text, fields FROM texts WHERE object_id IN ("+objects+")", listed,
		func(rows *sql.Rows) error {
			var objectID, text, encoded string
			if err := rows.Scan(&objectID, &text, &encoded); err != nil {
				return err
			}
			var fields map[string]string
			if err := json.Unmarshal([]byte(encoded), &fields); err != nil {
				return err
			}
			haystacks[objectID] = append(haystacks[objectID], text)
			for field, value := range fields {
				sensitive[objectID] = addValue(haystacks, sensitive[objectID], p, objectID, field, value)
			}
			return nil
		})
	if err != nil {
		return hits, err
	}
	err = s.eachRow(ctx, "SELECT object_id, field, value FROM text_corrections WHERE object_id IN ("+objects+")", listed,
		func(rows *sql.Rows) error {
			var objectID, field, value string
			if err := rows.Scan(&objectID, &field, &value); err != nil {
				return err
			}
			sensitive[objectID] = addValue(haystacks, sensitive[objectID], p, objectID, field, value)
			return nil
		})
	if err != nil {
		return hits, err
	}
	for objectID, haystack := range haystacks {
		if p.Assistant && len(haystack) > 0 {
			// The whole text, as the assistant sees it.
			haystack[0] = maskText(haystack[0], sensitive[objectID], func(string, string) string { return "\x00" })
		}
		if slices.ContainsFunc(haystack, func(text string) bool { return strings.Contains(fold(text), needle) }) {
			hits.objects[objectID] = true
		}
	}
	return hits, nil
}

// eachRow runs query and hands each row of its result to row, stopping at the
// first error.
func (s *Service) eachRow(ctx context.Context, query string, args []any, row func(*sql.Rows) error) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := row(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// addValue adds a field's value to what an object's search looks in, except
// a sensitive one for an assistant, which goes to the values to mask instead.
func addValue(haystacks map[string][]string, sensitive []fieldValue, p Principal, objectID, field, value string) []fieldValue {
	if _, secret := textread.Sensitive(field); secret && p.Assistant {
		return append(sensitive, fieldValue{field, value})
	}
	if _, read := haystacks[objectID]; !read {
		// A correction of a photo not read again yet: no whole text first.
		haystacks[objectID] = []string{""}
	}
	haystacks[objectID] = append(haystacks[objectID], value)
	return sensitive
}
