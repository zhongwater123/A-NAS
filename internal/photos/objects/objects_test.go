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
