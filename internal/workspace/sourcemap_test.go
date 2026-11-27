package workspace_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/workspace"
)

// sampleHelper stands for the helper script in the shared sample, which the
// web tests read too (docs/architecture/shared-workspace.md, 跨进程接缝).
const sampleHelper = `<script src="/assets/workspace-frame.js"></script>`

var anchor = regexp.MustCompile(` data-anas-at="[0-9]+"`)

func readSample(t *testing.T) []byte {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata", "prd.html"))
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// The annotated sample is checked in: the frame helper's tests read it to
// pick elements the way the browser will. Run with UPDATE_SAMPLES=1 to
// rewrite it after changing Annotate.
func TestTheAnnotatedSampleIsWhatAnnotateWrites(t *testing.T) {
	source := readSample(t)
	annotated := workspace.Annotate(source, sampleHelper)
	golden := filepath.Join("testdata", "prd.annotated.html")
	if os.Getenv("UPDATE_SAMPLES") == "1" {
		if err := os.WriteFile(golden, annotated, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(annotated, want) {
		t.Fatal("Annotate no longer writes testdata/prd.annotated.html; rerun with UPDATE_SAMPLES=1 and check the web tests")
	}
}

func TestAnnotateMarksEveryWrittenElementAndKeepsTheRestOfTheSource(t *testing.T) {
	source := readSample(t)
	annotated := workspace.Annotate(source, sampleHelper)
	if !bytes.HasPrefix(annotated, []byte("<!DOCTYPE html>"+sampleHelper)) {
		t.Fatalf("the helper is not right after the doctype: %q", annotated[:80])
	}
	stripped := anchor.ReplaceAll(bytes.Replace(annotated, []byte(sampleHelper), nil, 1), nil)
	if !bytes.Equal(stripped, source) {
		t.Fatal("without its anchors and the helper the annotated page is not the source")
	}
	spans := workspace.Spans(source)
	marks := anchor.FindAll(annotated, -1)
	if len(marks) != len(spans) || len(marks) < 100 {
		t.Fatalf("%d anchors for %d elements", len(marks), len(spans))
	}
	for start := range spans {
		if source[start] != '<' {
			t.Fatalf("an element starts at %d on %q", start, source[start])
		}
	}
}

func TestAnnotateWithoutADoctypePutsTheHelperFirst(t *testing.T) {
	got := string(workspace.Annotate([]byte("<p>hi</p>"), "<script></script>"))
	if got != `<script></script><p data-anas-at="0">hi</p>` {
		t.Fatalf("Annotate() = %q", got)
	}
	if got := string(workspace.Annotate([]byte("just text"), "<s></s>")); got != "just text<s></s>" {
		t.Fatalf("Annotate(text) = %q", got)
	}
}

func TestAPickedButtonNamesItsLinesAndItsRules(t *testing.T) {
	source := readSample(t)
	at := bytes.Index(source, []byte(`<button class="btn btn-primary">`))
	element, ok := workspace.Element(source, at)
	if !ok || element.Text != `<button class="btn btn-primary">发出分享</button>` || element.StartLine != 108 || element.EndLine != 108 {
		t.Fatalf("Element() = %+v, %v", element, ok)
	}
	actions := bytes.Index(source, []byte(`<div class="actions">`))
	if element, ok := workspace.Element(source, actions); !ok || element.StartLine != 106 || element.EndLine != 109 {
		t.Fatalf("the button row = %+v, %v", element, ok)
	}
	if _, ok := workspace.Element(source, at+1); ok {
		t.Fatal("an element starts inside a tag")
	}
	rules := workspace.CSSRules(source, []string{".btn", ".btn-primary", ".nowhere"})
	if rule := rules[".btn"]; rule.Line != 44 || !strings.HasPrefix(rule.Text, ".btn { border: 0;") || !strings.HasSuffix(rule.Text, "}") {
		t.Fatalf("CSSRules(.btn) = %+v", rule)
	}
	if rule, ok := rules[".btn-primary"]; !ok || !strings.Contains(rule.Text, "var(--brand)") {
		t.Fatalf("CSSRules(.btn-primary) = %+v %v", rule, ok)
	}
	if rule, ok := rules[".nowhere"]; ok {
		t.Fatalf("a selector the page does not write was found: %+v", rule)
	}
}

// A rule ends at its own closing brace, not at one in a string, a comment or
// a nested rule, and is found as the browser writes its selector.
func TestCSSRulesReadTheStyleSheetsAsCSS(t *testing.T) {
	source := []byte(`<p>.label { not a rule }</p>
<style>
/* .label { } */
.label { content: "}"; color: red; }
@media (max-width: 600px) {
  .a>.b,h1 { margin: 0 }
}
.card { color: blue; & .title { color: red } }
</style>`)
	rules := workspace.CSSRules(source, []string{".label", ".a > .b, h1", ".card", ".missing"})
	want := map[string]workspace.CSSRuleSource{
		".label":      {Line: 4, Text: `.label { content: "}"; color: red; }`},
		".a > .b, h1": {Line: 6, Text: ".a>.b,h1 { margin: 0 }"},
		".card":       {Line: 8, Text: ".card { color: blue; & .title { color: red } }"},
	}
	if len(rules) != len(want) {
		t.Fatalf("CSSRules() = %+v", rules)
	}
	for selector, rule := range want {
		if rules[selector] != rule {
			t.Errorf("CSSRules(%q) = %+v, want %+v", selector, rules[selector], rule)
		}
	}
}

func TestSpansCloseElementsWhoseEndTagIsImplied(t *testing.T) {
	source := []byte("<ul><li>a<li>b</ul><br><img src=x /><p>open")
	spans := workspace.Spans(source)
	for start, want := range map[string]string{
		"<ul>":    "<ul><li>a<li>b</ul>",
		"<li>a":   "<li>a",
		"<li>b":   "<li>b",
		"<br>":    "<br>",
		"<img":    "<img src=x />",
		"<p>open": "<p>open",
	} {
		at := bytes.Index(source, []byte(start))
		if got := string(source[at:spans[at]]); got != want {
			t.Errorf("element at %q spans %q, want %q", start, got, want)
		}
	}
}

func TestDeepestPointsAtTheElementsAnEditChanged(t *testing.T) {
	source := []byte(`<main><p>one</p><div><span>two</span></div></main>`)
	positions := []int{bytes.Index(source, []byte("one")), bytes.Index(source, []byte("two")), bytes.Index(source, []byte("two")) + 1}
	got := workspace.Deepest(source, positions)
	want := []int{bytes.Index(source, []byte("<p>")), bytes.Index(source, []byte("<span>"))}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Deepest() = %v, want %v", got, want)
	}
}

func TestTheTitleIsThePagesOwn(t *testing.T) {
	if got := workspace.Title(readSample(t)); got != "家庭相册分享 PRD" {
		t.Fatalf("Title() = %q", got)
	}
	if got := workspace.Title([]byte("<title>\n  a   b </title>")); got != "a b" {
		t.Fatalf("Title() = %q", got)
	}
	if got := workspace.Title([]byte("<p>no title</p>")); got != "" {
		t.Fatalf("Title() = %q", got)
	}
}

func TestAgentsReadLongPagesInSegmentsWithInlineDataFolded(t *testing.T) {
	image := "data:image/png;base64," + strings.Repeat("QUJD", 600)
	source := []byte("line 1\n<img src=\"" + image + "\">\nline 3\nline 4\n")
	// Line 1 and the folded line 2 take 54 characters; line 3 would make 61.
	segment := workspace.ReadSegment(source, 1, 58)
	if segment.FirstLine != 1 || segment.LastLine != 2 || segment.NextLine != 3 || segment.TotalLines != 4 || segment.Folded != 1 {
		t.Fatalf("segment = %+v", segment)
	}
	if !strings.Contains(segment.Text, "data:image/png;base64,…(1 KB, #1)") || strings.Contains(segment.Text, "QUJD") {
		t.Fatalf("the data URL is not folded: %q", segment.Text)
	}
	last := workspace.ReadSegment(source, 3, 1000)
	if last.Text != "line 3\nline 4\n" || last.NextLine != 0 || last.LastLine != 4 {
		t.Fatalf("last segment = %+v", last)
	}
	long := workspace.ReadSegment([]byte(strings.Repeat("长", 50)+"\nnext\n"), 1, 10)
	if !long.Truncated || long.Text != strings.Repeat("长", 10) || long.NextLine != 2 {
		t.Fatalf("a line longer than a segment = %+v", long)
	}
	if beyond := workspace.ReadSegment(source, 9, 100); beyond.Text != "" || beyond.TotalLines != 4 {
		t.Fatalf("reading past the end = %+v", beyond)
	}
}

func FuzzAnnotateKeepsTheSourceAndMarksOnlyTags(f *testing.F) {
	for _, seed := range []string{"<!doctype html><p>a", "<ul><li>a<li>b</ul>", "<script>if (a<b) {}</script><p>", "<!-- <p> -->", "<a href='<p>'>x</a>", "<svg><path/></svg>", "<textarea><b></textarea>", "<style>a { b: \"}\" } @media x { .b { } }</style>"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, source []byte) {
		annotated := workspace.Annotate(source, sampleHelper)
		stripped := anchor.ReplaceAll(bytes.Replace(annotated, []byte(sampleHelper), nil, 1), nil)
		if !bytes.Equal(stripped, source) && !anchor.Match(source) {
			t.Fatalf("annotating changed the source:\n%q\n%q", source, stripped)
		}
		for start, end := range workspace.Spans(source) {
			if start < 0 || end > len(source) || start >= end || source[start] != '<' {
				t.Fatalf("span %d–%d in %q", start, end, source)
			}
		}
		for selector, rule := range workspace.CSSRules(source, []string{"a", ".b", "p > a"}) {
			if !bytes.Contains(source, []byte(rule.Text)) || rule.Line < 1 {
				t.Fatalf("rule for %q is %+v, not the source's own", selector, rule)
			}
		}
	})
}
