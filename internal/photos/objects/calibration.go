package objects

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed v1.calibration.json
var calibrationV1 []byte

// Calibration says how search uses one detector model's boxes, as the
// evaluation measured it (ai/eval/hybrid_search.py writes the file;
// docs/research/photo-ai-object-detection.md).
type Calibration struct {
	// Model is the detector the calibration was measured for; boxes of any
	// other model take no part in search.
	Model string `json:"model"`
	// Weight scales the detector score added to a photo's similarity, times
	// how much of the query the enabled words make up.
	Weight float64 `json:"weight"`
	// TierCoverage is how much of a query its words must make up for the
	// photos the detector found them in to come first.
	TierCoverage float64 `json:"tierCoverage"`
	// Fusion lists the classes a word never measured on its own may name and
	// still take part.
	Fusion []int `json:"fusion"`
	// Words holds what was measured for a word, keyed by the classes it names
	// in ascending order ("6,35,50,88,127,309" for 汽车). Its synonyms name
	// the same classes and share it.
	Words map[string]WordCalibration `json:"words"`

	fusion map[int]bool
}

// WordCalibration is what the evaluation measured for one word.
type WordCalibration struct {
	// Fusion is false when adding the detector made ranking worse; the word is
	// still matched, so that 热狗 is not 狗, but adds nothing.
	Fusion bool `json:"fusion"`
	// TierScore is the detector score from which a photo counts as showing
	// the thing, measured to be right often enough and to find enough of the
	// photos; zero when no score was.
	TierScore float64 `json:"tierScore"`
}

// V1Calibration returns the calibration built into this program.
func V1Calibration() (Calibration, error) { return ParseCalibration(calibrationV1) }

// ParseCalibration reads a calibration and checks that it fits the classes.
func ParseCalibration(data []byte) (Calibration, error) {
	var c Calibration
	if err := json.Unmarshal(data, &c); err != nil {
		return Calibration{}, fmt.Errorf("object calibration: %w", err)
	}
	if c.Model == "" || c.Weight <= 0 || c.Weight > 1 || c.TierCoverage <= 0 || c.TierCoverage > 1 {
		return Calibration{}, fmt.Errorf("object calibration needs a model, a weight in (0, 1] and a tier coverage in (0, 1]")
	}
	c.fusion = map[int]bool{}
	for _, class := range c.Fusion {
		if class <= 0 || class >= Classes {
			return Calibration{}, fmt.Errorf("fusion class %d is out of range", class)
		}
		c.fusion[class] = true
	}
	for key, word := range c.Words {
		classes, err := parseWordKey(key)
		if err != nil {
			return Calibration{}, err
		}
		for i := 1; i < len(classes); i++ {
			if classes[i] <= classes[i-1] {
				return Calibration{}, fmt.Errorf("word %q must list its classes once, in ascending order", key)
			}
		}
		if word.TierScore < 0 || word.TierScore >= 1 || word.TierScore > 0 && !word.Fusion {
			return Calibration{}, fmt.Errorf("word %q has tier score %v; it needs one in [0, 1) and fusion", key, word.TierScore)
		}
	}
	return c, nil
}

func parseWordKey(key string) ([]int, error) {
	var classes []int
	for _, part := range strings.Split(key, ",") {
		class, err := strconv.Atoi(part)
		if err != nil || class <= 0 || class >= Classes {
			return nil, fmt.Errorf("word %q names a class out of range", key)
		}
		classes = append(classes, class)
	}
	return classes, nil
}

func wordKey(classes []int) string {
	parts := make([]string, len(classes))
	for i, class := range classes {
		parts[i] = strconv.Itoa(class)
	}
	return strings.Join(parts, ",")
}

// word returns what was measured for a mention, if anything.
func (c Calibration) word(mention Mention) (WordCalibration, bool) {
	word, ok := c.Words[wordKey(mention.Classes)]
	return word, ok
}

// Enabled returns the mentions that take part in search: a word measured on
// its own when it did not hurt, any other when all its classes are on the
// fusion list.
func (c Calibration) Enabled(mentions []Mention) []Mention {
	var enabled []Mention
	for _, mention := range mentions {
		word, measured := c.word(mention)
		ok := word.Fusion
		if !measured {
			ok = true
			for _, class := range mention.Classes {
				ok = ok && c.fusion[class]
			}
		}
		if ok {
			enabled = append(enabled, mention)
		}
	}
	return enabled
}

// TierScores returns, for a query about things the detector finds reliably,
// the score each mention's best class must reach for a photo to count as
// showing it; nil for any other query. Such a query mentions only enabled
// words that have a tier score, and they make up at least TierCoverage of it.
func (c Calibration) TierScores(text string, mentions []Mention) []float64 {
	if len(mentions) == 0 || len(c.Enabled(mentions)) != len(mentions) || Coverage(text, mentions) < c.TierCoverage {
		return nil
	}
	scores := make([]float64, len(mentions))
	for i, mention := range mentions {
		word, _ := c.word(mention)
		if word.TierScore <= 0 {
			return nil
		}
		scores[i] = word.TierScore
	}
	return scores
}
