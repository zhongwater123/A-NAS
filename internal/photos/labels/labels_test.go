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

func TestMentionedFindsLabelsByTheirLongestWord(t *testing.T) {
	set, err := labels.V1()
	if err != nil {
		t.Fatal(err)
	}
	ids := func(text string) []string {
		var found []string
		for _, label := range set.Mentioned(text) {
			found = append(found, label.ID)
		}
		return found
	}
	for text, want := range map[string][]string{
		"笔记本电脑":         {"laptop"},
		"海边的小猫":         {"sea", "cat"},
		"热狗":            {"hot_dog"},
		"猫头鹰和大熊猫":       {"owl", "panda"},
		"猫和猫咪":          {"cat"},
		"停在楼下的电动车":      {"electric_bike"},
		"Model 3D 1024": nil,
	} {
		if got := ids(text); !slices.Equal(got, want) {
			t.Errorf("Mentioned(%q) = %v, want %v", text, got, want)
		}
	}
}
