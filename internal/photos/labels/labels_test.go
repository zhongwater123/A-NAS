package labels_test

import (
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos/labels"
)

func TestBuiltInSetIsConsistent(t *testing.T) {
	set, err := labels.V1()
	if err != nil {
		t.Fatalf("V1() error = %v", err)
	}
	if len(set.Labels) < 200 || len(set.Shown()) == 0 || len(set.Shown()) >= len(set.Labels) {
		t.Fatalf("%d labels, %d shown", len(set.Labels), len(set.Shown()))
	}
	for _, label := range set.Shown() {
		if len(set.Texts(label)) == 0 {
			t.Fatalf("label %s has no text", label.ID)
		}
	}
}

func TestParseRejectsCalibrationsThatDoNotFit(t *testing.T) {
	vocabulary := []byte(`{"version": 1, "labels": [{"id": "cat", "name": "猫", "synonyms": ["猫咪"], "category": "animal"}]}`)
	good := `{"labels": 1, "model": "m", "phrase": "一张{}的照片", "synonyms": true, "thresholds": {"cat": 0.7}}`
	set, err := labels.Parse(vocabulary, []byte(good))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := set.Texts(set.Labels[0]); !slices.Equal(got, []string{"一张猫的照片", "一张猫咪的照片"}) {
		t.Fatalf("Texts() = %v", got)
	}
	for name, calibration := range map[string]string{
		"other label set": `{"labels": 2, "model": "m", "phrase": "{}", "thresholds": {}}`,
		"no model":        `{"labels": 1, "phrase": "{}", "thresholds": {}}`,
		"bad phrase":      `{"labels": 1, "model": "m", "phrase": "猫", "thresholds": {}}`,
		"unknown label":   `{"labels": 1, "model": "m", "phrase": "{}", "thresholds": {"dog": 0.7}}`,
		"out of range":    `{"labels": 1, "model": "m", "phrase": "{}", "thresholds": {"cat": 1.5}}`,
	} {
		if _, err := labels.Parse(vocabulary, []byte(calibration)); err == nil {
			t.Errorf("Parse() accepted a calibration with %s", name)
		}
	}
}
