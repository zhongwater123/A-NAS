// Package labels is the versioned vocabulary of AI labels and the thresholds
// calibrated for one model on public datasets
// (docs/research/photo-ai-label-calibration.md). A label's score is the
// similarity of a photo's vector to the label's text vector; a photo shows the
// label only above the label's threshold, and only for the calibrated model.
package labels

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

//go:embed v1.json
var vocabularyV1 []byte

//go:embed v1.calibration.json
var calibrationV1 []byte

type Label struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Synonyms []string `json:"synonyms"`
	Category string   `json:"category"`
}

// Set is a vocabulary together with its calibration.
type Set struct {
	Version int
	Labels  []Label
	// Model is the model the thresholds were calibrated for; with any other
	// model no label is shown.
	Model string
	// Phrase turns a label word into the query text the AI Worker encodes,
	// such as "{}" or "一张{}的照片".
	Phrase string
	// Synonyms says whether a label's vector averages its synonyms' too.
	Synonyms bool
	// Thresholds holds the labels that may be shown; the rest only take part
	// in search.
	Thresholds map[string]float64
}

// V1 returns the vocabulary and calibration built into this program.
func V1() (Set, error) { return Parse(vocabularyV1, calibrationV1) }

// Parse reads a vocabulary and its calibration and checks that they agree.
func Parse(vocabulary, calibration []byte) (Set, error) {
	var v struct {
		Version int     `json:"version"`
		Labels  []Label `json:"labels"`
	}
	if err := json.Unmarshal(vocabulary, &v); err != nil {
		return Set{}, fmt.Errorf("label vocabulary: %w", err)
	}
	var c struct {
		Labels     int                `json:"labels"`
		Model      string             `json:"model"`
		Phrase     string             `json:"phrase"`
		Synonyms   bool               `json:"synonyms"`
		Thresholds map[string]float64 `json:"thresholds"`
	}
	if err := json.Unmarshal(calibration, &c); err != nil {
		return Set{}, fmt.Errorf("label calibration: %w", err)
	}
	if c.Labels != v.Version {
		return Set{}, fmt.Errorf("calibration for label set %d, vocabulary is %d", c.Labels, v.Version)
	}
	if c.Model == "" || strings.Count(c.Phrase, "{}") != 1 {
		return Set{}, fmt.Errorf("label calibration needs a model and a phrase with one {}")
	}
	known := make(map[string]bool, len(v.Labels))
	for _, label := range v.Labels {
		if label.ID == "" || label.Name == "" || known[label.ID] {
			return Set{}, fmt.Errorf("label %q is empty or repeated", label.ID)
		}
		known[label.ID] = true
	}
	for id, threshold := range c.Thresholds {
		if !known[id] || threshold <= -1 || threshold >= 1 {
			return Set{}, fmt.Errorf("threshold for %q is not for a known label or out of range", id)
		}
	}
	return Set{
		Version: v.Version, Labels: v.Labels, Model: c.Model, Phrase: c.Phrase,
		Synonyms: c.Synonyms, Thresholds: c.Thresholds,
	}, nil
}

// Texts returns the query texts whose vectors average into the label's.
func (s Set) Texts(label Label) []string {
	words := []string{label.Name}
	if s.Synonyms {
		words = append(words, label.Synonyms...)
	}
	texts := make([]string, len(words))
	for i, word := range words {
		texts[i] = strings.Replace(s.Phrase, "{}", word, 1)
	}
	return texts
}

// Mentioned returns the labels a text names by their name or a synonym, in
// order and each once. Where words overlap the longest wins, so 热狗 names the
// hot dog and not the dog; case is ignored.
func (s Set) Mentioned(text string) []Label {
	type term struct {
		word  string
		label int
	}
	var terms []term
	for i, label := range s.Labels {
		for _, word := range append([]string{label.Name}, label.Synonyms...) {
			if word = strings.ToLower(word); word != "" {
				terms = append(terms, term{word, i})
			}
		}
	}
	text = strings.ToLower(text)
	var found []Label
	seen := make(map[string]bool)
	for position := 0; position < len(text); {
		longest := -1
		for i, candidate := range terms {
			if strings.HasPrefix(text[position:], candidate.word) && (longest < 0 || len(candidate.word) > len(terms[longest].word)) {
				longest = i
			}
		}
		if longest < 0 {
			_, size := utf8.DecodeRuneInString(text[position:])
			position += size
			continue
		}
		label := s.Labels[terms[longest].label]
		if !seen[label.ID] {
			seen[label.ID] = true
			found = append(found, label)
		}
		position += len(terms[longest].word)
	}
	return found
}

// Shown returns the labels that have a threshold, in vocabulary order.
func (s Set) Shown() []Label {
	var shown []Label
	for _, label := range s.Labels {
		if _, ok := s.Thresholds[label.ID]; ok {
			shown = append(shown, label)
		}
	}
	return shown
}
