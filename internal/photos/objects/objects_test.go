package objects_test

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/photos/objects"
)

func matcher(t *testing.T) *objects.Matcher {
	t.Helper()
	table, err := objects.V1()
	if err != nil {
		t.Fatalf("V1() error = %v", err)
	}
	return objects.NewMatcher(table)
}

func TestBuiltInTableIsConsistent(t *testing.T) {
	table, err := objects.V1()
	if err != nil {
		t.Fatalf("V1() error = %v", err)
	}
	if len(table.Classes) != objects.Classes-1 {
		t.Fatalf("%d classes, want %d", len(table.Classes), objects.Classes-1)
	}
}

func TestParseRejectsTablesThatDoNotFit(t *testing.T) {
	for name, table := range map[string]string{
		"class out of range": `{"classes": [{"id": 366, "en": "X", "zh": "叉"}]}`,
		"unnamed class":      `{"classes": [{"id": 1, "en": "Person", "zh": ""}]}`,
		"repeated class":     `{"classes": [{"id": 1, "en": "A", "zh": "甲"}, {"id": 1, "en": "B", "zh": "乙"}]}`,
		"unknown group id":   `{"classes": [{"id": 1, "en": "A", "zh": "甲"}], "groups": {"组": [2]}}`,
		"empty group":        `{"classes": [{"id": 1, "en": "A", "zh": "甲"}], "groups": {"组": []}}`,
	} {
		if _, err := objects.Parse([]byte(table)); err == nil {
			t.Errorf("Parse() accepted a table with %s", name)
		}
	}
}

func TestMentionsTakeTheLongestWordAndSkipIgnoredWords(t *testing.T) {
	m := matcher(t)
	words := func(text string) []string {
		var found []string
		for _, mention := range m.Mentions(text) {
			found = append(found, mention.Word)
		}
		return found
	}
	for text, want := range map[string][]string{
		"热狗":            {"热狗"},
		"马路上的车":         nil,
		"桌上的手机":         {"手机"},
		"笔记本电脑":         {"笔记本电脑"},
		"苹果手机":          {"苹果手机"},
		"iPad 和键盘":      {"ipad", "键盘"},
		"Model 3D 1024": nil,
	} {
		if got := words(text); !slices.Equal(got, want) {
			t.Errorf("Mentions(%q) = %v, want %v", text, got, want)
		}
	}
	if got := m.Mentions("鸟"); len(got) != 1 || len(got[0].Classes) < 5 {
		t.Errorf("Mentions(鸟) = %v, want the group of birds", got)
	}
}

func TestCoverageCountsCharactersWithoutPunctuation(t *testing.T) {
	m := matcher(t)
	for text, want := range map[string]float64{
		"手机":       1,
		"  手机  ":   1,
		"桌上的手机":    0.4,
		"“海边”的日落！": 0,
		"":         0,
		"笔记本电脑和鼠标": 7.0 / 8,
	} {
		if got := objects.Coverage(text, m.Mentions(text)); math.Abs(got-want) > 1e-9 {
			t.Errorf("Coverage(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestBuiltInCalibrationFitsTheTable(t *testing.T) {
	calibration, err := objects.V1Calibration()
	if err != nil {
		t.Fatalf("V1Calibration() error = %v", err)
	}
	tiered := 0
	for _, word := range calibration.Words {
		if word.TierScore > 0 {
			tiered++
		}
	}
	if len(calibration.Fusion) == 0 || len(calibration.Words) == 0 || tiered == 0 {
		t.Fatalf("%d fusion classes, %d measured words, %d with a tier score", len(calibration.Fusion), len(calibration.Words), tiered)
	}
}

func TestCalibrationDecidesWhichWordsCount(t *testing.T) {
	for name, text := range map[string]string{
		"no model":            `{"weight": 0.2, "tierCoverage": 0.6}`,
		"weight out of range": `{"model": "m", "weight": 2, "tierCoverage": 0.6}`,
		"class out of range":  `{"model": "m", "weight": 0.2, "tierCoverage": 0.6, "fusion": [366]}`,
		"word out of range":   `{"model": "m", "weight": 0.2, "tierCoverage": 0.6, "words": {"366": {"fusion": true}}}`,
		"word out of order":   `{"model": "m", "weight": 0.2, "tierCoverage": 0.6, "words": {"35,6": {"fusion": true}}}`,
		"tier without fusion": `{"model": "m", "weight": 0.2, "tierCoverage": 0.6, "words": {"62": {"fusion": false, "tierScore": 0.6}}}`,
		"tier score of one":   `{"model": "m", "weight": 0.2, "tierCoverage": 0.6, "words": {"62": {"fusion": true, "tierScore": 1}}}`,
	} {
		if _, err := objects.ParseCalibration([]byte(text)); err == nil {
			t.Errorf("ParseCalibration() accepted a calibration with %s", name)
		}
	}
	// 手机 and 猫 were measured, 汽车 too as the group of car kinds it names, 花
	// was measured to hurt; 狗 never was, but its class is on the fusion list.
	calibration, err := objects.ParseCalibration([]byte(`{"model": "m", "weight": 0.2, "tierCoverage": 0.6,
		"fusion": [62, 140, 93], "words": {"62": {"fusion": true, "tierScore": 0.6}, "140": {"fusion": true},
		"6,35,50,88,127,309": {"fusion": true, "tierScore": 0.55}, "24": {"fusion": false}}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := matcher(t)
	enabled := func(text string) []string {
		var words []string
		for _, mention := range calibration.Enabled(m.Mentions(text)) {
			words = append(words, mention.Word)
		}
		return words
	}
	for text, want := range map[string][]string{
		"手机和猫": {"手机", "猫"},
		"花和杯子": nil, // 花 hurt; cups were not measured and are not fused
		"宠物":   nil, // the group covers rabbits and parrots too, never measured
		"猫和狗":  {"猫", "狗"},
		"汽车":   {"汽车"}, // stands on its own measurement, though SUVs were never measured
	} {
		if got := enabled(text); !slices.Equal(got, want) {
			t.Errorf("Enabled(%q) = %v, want %v", text, got, want)
		}
	}
	for text, want := range map[string][]float64{
		"手机":      {0.6},
		"汽车":      {0.55},
		"桌上的手机":   nil, // too little of the query
		"手机和猫":    nil, // no score finds cats reliably
		"狗":       nil, // never measured
		"拍日落用的手机": nil,
	} {
		if got := calibration.TierScores(text, m.Mentions(text)); !slices.Equal(got, want) {
			t.Errorf("TierScores(%q) = %v, want %v", text, got, want)
		}
	}
}

// The evaluation measured the Python matcher; eval.matcher_golden writes what
// it finds, and Go must find the same.
func TestMatcherAgreesWithTheEvaluation(t *testing.T) {
	data, err := os.ReadFile("testdata/matcher_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Query    string `json:"query"`
		Mentions []struct {
			Word    string `json:"word"`
			Classes []int  `json:"classes"`
		} `json:"mentions"`
		Coverage float64 `json:"coverage"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 150 {
		t.Fatalf("%d golden queries, want at least 150", len(cases))
	}
	m := matcher(t)
	for _, c := range cases {
		got := m.Mentions(c.Query)
		if len(got) != len(c.Mentions) {
			t.Errorf("Mentions(%q) = %v, want %v", c.Query, got, c.Mentions)
			continue
		}
		for i, mention := range got {
			if mention.Word != c.Mentions[i].Word || !slices.Equal(mention.Classes, c.Mentions[i].Classes) {
				t.Errorf("Mentions(%q)[%d] = %v, want %v", c.Query, i, mention, c.Mentions[i])
			}
		}
		if coverage := objects.Coverage(c.Query, got); math.Abs(coverage-c.Coverage) > 1e-6 {
			t.Errorf("Coverage(%q) = %v, want %v", c.Query, coverage, c.Coverage)
		}
	}
}
