package photos

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/zhongwater123/A-NAS/internal/photos/textread"
	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// textHits are the photos whose text holds a search's query: the content
// objects found, seen in the libraries of the search that read text.
type textHits struct {
	objects, libraries map[string]bool
}

func (t textHits) holds(c candidate) bool { return t.libraries[c.libraryID] && t.objects[c.objectID] }

// textMatches finds the content objects whose text holds query, among the
// photos listed to p in the libraries of scope that read text
// (docs/architecture/photo-ocr.md#搜索): the whole text, and the fields as
// the photo's text shows them, read and corrected, and the document type it
// was read as, by the words members call it (身份证, 发票). It compares regardless of
// spaces, width and case, and a query mostly of digits also regardless of
// hyphens, so that "6222 0200" finds 6222-0200-…. A query of one character
// finds nothing, for nearly every text holds a common one. An assistant
// matches only the text as it sees it, every sensitive value masked:
// searching for parts of a number would otherwise tell it the number a digit
// at a time.
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
	if utf8.RuneCountInString(needle) < 2 {
		return hits, nil
	}
	listed := make([]any, len(readers))
	for i, id := range readers {
		listed[i] = id
	}
	objects := "SELECT a.object_id FROM assets a WHERE " + listedAssets(p, "a") +
		" AND a.library_id IN (" + sqlitedb.Placeholders(len(listed)) + ")"
	type found struct {
		kind, text  string
		fields      map[string]string
		corrections map[string]string
	}
	readings := map[string]*found{}
	err = s.eachRow(ctx, "SELECT object_id, kind, text, fields FROM texts WHERE object_id IN ("+objects+")", listed,
		func(rows *sql.Rows) error {
			var objectID, encoded string
			reading := &found{corrections: map[string]string{}}
			if err := rows.Scan(&objectID, &reading.kind, &reading.text, &encoded); err != nil {
				return err
			}
			readings[objectID] = reading
			return json.Unmarshal([]byte(encoded), &reading.fields)
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
			// A photo without a reading shows no correction.
			if reading := readings[objectID]; reading != nil {
				reading.corrections[field] = value
			}
			return nil
		})
	if err != nil {
		return hits, err
	}
	holds := func(text string) bool { return strings.Contains(fold(text), needle) }
	for objectID, reading := range readings {
		// The fields the photo's text shows, as Text gives them.
		var fields []TextField
		for _, name := range textread.FieldNames(textread.Kind(reading.kind)) {
			field := TextField{Name: name, Read: reading.fields[name], Value: reading.fields[name]}
			if corrected, ok := reading.corrections[name]; ok {
				field.Value = corrected
			}
			_, field.Sensitive = textread.Sensitive(name)
			fields = append(fields, field)
		}
		texts := []string{reading.text}
		for _, field := range fields {
			if !p.Assistant || !field.Sensitive {
				texts = append(texts, field.Value, field.Read)
			}
		}
		if p.Assistant {
			masker := newTextMasker(fields, func(string, string) string { return "\x00" })
			for i, text := range texts {
				texts[i] = masker.mask(text)
			}
		}
		// The type a photo was read as is found by its name too: no ID card
		// prints 身份证 on its front. The type is no secret to an assistant.
		if textread.Named(textread.Kind(reading.kind), needle) || slices.ContainsFunc(texts, holds) {
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
