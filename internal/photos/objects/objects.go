// Package objects is the Chinese vocabulary of the object detector's classes
// and the matcher that finds which classes a search query names
// (docs/architecture/photo-objects.md). The detector reports Objects365
// classes by number; a query names a class by its Chinese name, a synonym or
// a word that covers several classes (鞋子, 水果). The longest word wins, so
// 热狗 names the hot dog and not the dog, and words such as 马路 are skipped
// whole so that they name nothing. The evaluation (ai/eval/hybrid_search.py)
// reads the same table and matches the same way.
package objects

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

//go:embed v1.json
var tableV1 []byte

// Classes is the number of class slots the detector reports, the unused
// slot 0 included.
const Classes = 366

// Class is one detector class.
type Class struct {
	ID       int      `json:"id"`
	English  string   `json:"en"`
	Name     string   `json:"zh"`
	Synonyms []string `json:"synonyms"`
	// Note marks a class whose meaning is not certain from its English name.
	Note string `json:"note,omitempty"`
}

// Table is the vocabulary: the classes, the words that cover several of them
// and the words that name nothing.
type Table struct {
	Classes []Class          `json:"classes"`
	Groups  map[string][]int `json:"groups"`
	Ignore  []string         `json:"ignore"`
}

// V1 returns the table built into this program.
func V1() (Table, error) { return Parse(tableV1) }

// Parse reads a table and checks that every word names known classes once.
func Parse(data []byte) (Table, error) {
	var table Table
	if err := json.Unmarshal(data, &table); err != nil {
		return Table{}, fmt.Errorf("object classes: %w", err)
	}
	known := make(map[int]bool, len(table.Classes))
	for _, class := range table.Classes {
		if class.ID <= 0 || class.ID >= Classes || class.Name == "" || known[class.ID] {
			return Table{}, fmt.Errorf("class %d (%q) is out of range, unnamed or repeated", class.ID, class.English)
		}
		known[class.ID] = true
	}
	for word, ids := range table.Groups {
		if word == "" || len(ids) == 0 {
			return Table{}, fmt.Errorf("group %q is empty", word)
		}
		for _, id := range ids {
			if !known[id] {
				return Table{}, fmt.Errorf("group %q names unknown class %d", word, id)
			}
		}
	}
	return table, nil
}

// Mention is a word of a query and the classes it names.
type Mention struct {
	Word    string
	Classes []int
}

// Matcher finds the classes a text names.
type Matcher struct {
	// words, longest first; an ignored word has no classes.
	words []matcherWord
}

type matcherWord struct {
	runes   []rune
	classes []int
}

// NewMatcher indexes the table's names, synonyms, group words and ignored
// words. A word that is both a name and a group word names all their classes.
func NewMatcher(table Table) *Matcher {
	classes := make(map[string]map[int]bool)
	add := func(word string, ids ...int) {
		word = strings.ToLower(word)
		if word == "" {
			return
		}
		if classes[word] == nil {
			classes[word] = make(map[int]bool)
		}
		for _, id := range ids {
			classes[word][id] = true
		}
	}
	for _, class := range table.Classes {
		for _, word := range append([]string{class.Name}, class.Synonyms...) {
			add(word, class.ID)
		}
	}
	for word, ids := range table.Groups {
		add(word, ids...)
	}
	for _, word := range table.Ignore {
		// Ignored words are consumed whole and name nothing.
		classes[strings.ToLower(word)] = map[int]bool{}
	}
	m := &Matcher{}
	for word, set := range classes {
		ids := make([]int, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		m.words = append(m.words, matcherWord{runes: []rune(word), classes: ids})
	}
	slices.SortFunc(m.words, func(a, b matcherWord) int {
		if len(a.runes) != len(b.runes) {
			return len(b.runes) - len(a.runes)
		}
		return slices.Compare(a.runes, b.runes)
	})
	return m
}

// Mentions returns the words of text that name classes, in order. Case is
// ignored; ignored words are consumed but not returned.
func (m *Matcher) Mentions(text string) []Mention {
	runes := []rune(strings.ToLower(text))
	var found []Mention
	for position := 0; position < len(runes); {
		matched := false
		for _, word := range m.words {
			if len(word.runes) <= len(runes)-position && slices.Equal(runes[position:position+len(word.runes)], word.runes) {
				if len(word.classes) > 0 {
					found = append(found, Mention{Word: string(word.runes), Classes: word.classes})
				}
				position += len(word.runes)
				matched = true
				break
			}
		}
		if !matched {
			position++
		}
	}
	return found
}

// punctuation is not counted in a query's length.
const punctuation = " \t，。、！？；：,.!?;:\"'“”‘’（）()"

// Coverage is how much of text the mentioned words make up, by character:
// 1 for 手机, little for a sentence that names one thing among many.
func Coverage(text string, mentions []Mention) float64 {
	length := 0
	for _, char := range text {
		if !strings.ContainsRune(punctuation, char) {
			length++
		}
	}
	covered := 0
	for _, mention := range mentions {
		covered += utf8.RuneCountInString(mention.Word)
	}
	return min(1, float64(covered)/float64(max(1, length)))
}
