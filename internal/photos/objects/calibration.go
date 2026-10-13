package objects

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed v1.calibration.json
var calibrationV1 []byte

// Calibration says how search uses one detector model's boxes, as the
// evaluation measured it (ai/eval/hybrid_search.py writes the file;
// docs/research/photo-ai-object-detection.md).
type Calibration struct {
	// Model is the detector the lists were measured for; boxes of any other
	// model take no part in search.
	Model string `json:"model"`
	// Weight scales the detector score added to a photo's similarity, times
	// how much of the query the enabled class words make up.
	Weight float64 `json:"weight"`
	// TierScore is the detector score at which a photo counts as showing the
	// thing, and TierCoverage how much of a query must be class words on the
	// tier list for those photos to come first.
	TierScore    float64 `json:"tierScore"`
	TierCoverage float64 `json:"tierCoverage"`
	// Fusion and Tier list the classes measured not to hurt ranking, and to
	// be found reliably enough to say whether a photo shows them.
	Fusion []int `json:"fusion"`
	Tier   []int `json:"tier"`
	// DisabledWords were measured to hurt: they are matched but add nothing.
	DisabledWords []string `json:"disabledWords"`

	fusion, tier map[int]bool
	disabled     map[string]bool
}

// V1Calibration returns the calibration built into this program.
func V1Calibration() (Calibration, error) { return ParseCalibration(calibrationV1) }

// ParseCalibration reads a calibration and checks that it fits the classes.
func ParseCalibration(data []byte) (Calibration, error) {
	var c Calibration
	if err := json.Unmarshal(data, &c); err != nil {
		return Calibration{}, fmt.Errorf("object calibration: %w", err)
	}
	if c.Model == "" || c.Weight <= 0 || c.Weight > 1 || c.TierScore <= 0 || c.TierScore >= 1 ||
		c.TierCoverage <= 0 || c.TierCoverage > 1 {
		return Calibration{}, fmt.Errorf("object calibration needs a model, a weight in (0, 1] and tier limits in (0, 1)")
	}
	c.fusion, c.tier, c.disabled = map[int]bool{}, map[int]bool{}, map[string]bool{}
	for _, class := range c.Fusion {
		if class <= 0 || class >= Classes {
			return Calibration{}, fmt.Errorf("fusion class %d is out of range", class)
		}
		c.fusion[class] = true
	}
	for _, class := range c.Tier {
		if !c.fusion[class] {
			return Calibration{}, fmt.Errorf("tier class %d is not on the fusion list", class)
		}
		c.tier[class] = true
	}
	for _, word := range c.DisabledWords {
		c.disabled[word] = true
	}
	return c, nil
}

// Enabled returns the mentions that take part in search: those whose classes
// are all on the fusion list and whose word was not found harmful.
func (c Calibration) Enabled(mentions []Mention) []Mention {
	var enabled []Mention
	for _, mention := range mentions {
		if c.disabled[mention.Word] {
			continue
		}
		all := true
		for _, class := range mention.Classes {
			all = all && c.fusion[class]
		}
		if all {
			enabled = append(enabled, mention)
		}
	}
	return enabled
}

// Tiered reports whether a query is about things the detector finds reliably:
// every word it mentions is enabled and on the tier list, and they make up at
// least TierCoverage of it.
func (c Calibration) Tiered(text string, mentions []Mention) bool {
	if len(mentions) == 0 || len(c.Enabled(mentions)) != len(mentions) || Coverage(text, mentions) < c.TierCoverage {
		return false
	}
	for _, mention := range mentions {
		for _, class := range mention.Classes {
			if !c.tier[class] {
				return false
			}
		}
	}
	return true
}
