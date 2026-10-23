package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeLinkTarget(t *testing.T) {
	tests := map[string]string{
		"guide.md#start":              "guide.md",
		"<a path/guide.md#start>":     "a path/guide.md",
		"guide.md \"optional title\"": "guide.md",
		"#local":                      "",
	}

	for input, want := range tests {
		if got := normalizeLinkTarget(input); got != want {
			t.Errorf("normalizeLinkTarget(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExternalLinks(t *testing.T) {
	for _, target := range []string{"https://example.com", "mailto:test@example.com", "/absolute/path"} {
		if !isExternalLink(target) {
			t.Errorf("isExternalLink(%q) = false", target)
		}
	}
	if isExternalLink("../CONTEXT.md") {
		t.Error("relative repository link classified as external")
	}
}

func TestValidateDocumentationCountsMarkdownAndFindsBrokenLinks(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		"docs/broken.md":                                "[missing](missing.md)\n",
		"web/node_modules/dependency/README.md":         "[dependency link](missing.md)\n",
		"internal/webui/dist/generated.md":              "[dependency link](missing.md)\n",
		".claude/worktrees/other-branch/docs/broken.md": "[other checkout](missing.md) 尚未部署\n",
	})

	problems, files := validateDocumentation(root)
	if files != len(requiredDocuments)+1 {
		t.Fatalf("validateDocumentation() counted %d Markdown files, want %d", files, len(requiredDocuments)+1)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "missing.md") {
		t.Fatalf("validateDocumentation() problems = %v, want one missing-link problem", problems)
	}
}

func TestValidateDocumentationCapsCurrentSize(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		currentDocument: "# 当前状态\n\n更新时间：2026-10-23\n\n" + strings.Repeat("- 流水账\n", currentMaxBytes/len("- 流水账\n")),
	})

	problems, _ := validateDocumentation(root)
	if len(problems) != 1 || !strings.Contains(problems[0], currentDocument) || !strings.Contains(problems[0], "cap") {
		t.Fatalf("validateDocumentation() problems = %v, want one size-cap problem", problems)
	}
}

func TestValidateDocumentationKeepsMergeAndDeployStatusInCurrent(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		currentDocument: "# 当前状态\n\n更新时间：2026-10-23\n\n- 已合并、尚未部署：#1\n",
		"docs/runbooks/accept-features-on-experimental-nas.md": "# 验收\n\n待合并的修复部署后再验收。\n",
		"docs/specs/music.md":           "# 音乐\n\n状态：implemented（尚未合并）\n",
		"docs/adr/0023-share-copies.md": "# 0023\n\n状态：proposed（待合并评审，尚未部署）\n",
		"docs/research/upstream.md":     "上游 PR 在 2026-10-16 查看时仍开着。\n",
	})
	writeDocuments(t, root, map[string]string{
		"docs/adr/README.md": "- [0023：分享](0023-share-copies.md)（proposed）\n",
	})

	problems, _ := validateDocumentation(root)
	want := []string{"docs/specs/music.md:3 says 未合并", "docs/adr/0023-share-copies.md:3 says 待合并"}
	if len(problems) != len(want) {
		t.Fatalf("validateDocumentation() problems = %v, want %v", problems, want)
	}
	for _, fragment := range want {
		if !containsFragment(problems, fragment) {
			t.Errorf("validateDocumentation() problems = %v, want one containing %q", problems, fragment)
		}
	}
}

func TestValidateDocumentationMatchesADRStatusWithIndex(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		"docs/adr/0001-accepted.md":       "# 0001\n\n状态：accepted（2026-10-10）\n",
		"docs/adr/0002-proposed.md":       "# 0002\n\n状态：proposed（2026-10-12，随 PR 提交评审）\n",
		"docs/adr/0003-unmarked.md":       "# 0003\n\n状态：proposed\n",
		"docs/adr/0004-overmarked.md":     "# 0004\n\n状态：accepted\n",
		"docs/adr/0005-superseded.md":     "# 0005\n\n状态：superseded by [0006](0006-no-status.md)\n",
		"docs/adr/0006-no-status.md":      "# 0006\n\nThe decision.\n",
		"docs/adr/0007-amended.md":        "# 0007\n\nStatus: amended by [0001](0001-accepted.md).\n",
		"docs/adr/0008-unlisted.md":       "# 0008\n\n状态：accepted\n",
		"docs/adr/0009-english-status.md": "# 0009\n\nStatus: Proposed\n",
		"docs/adr/README.md": "# ADR\n\n" +
			"- [0001：已接受](0001-accepted.md)（修订 0007）\n" +
			"- [0002：提议](0002-proposed.md)（proposed）\n" +
			"- [0003：漏标](0003-unmarked.md)\n" +
			"- [0004：多标](0004-overmarked.md)（proposed）\n" +
			"- [0005：已替代](0005-superseded.md)（superseded，见 0006）\n" +
			"- [0006：无状态](0006-no-status.md)\n" +
			"- [0007：已修订](0007-amended.md)（已被 0001 修订）\n" +
			"- [0009：英文状态](0009-english-status.md)（proposed）\n",
	})

	problems, _ := validateDocumentation(root)
	want := []string{
		"docs/adr/0003-unmarked.md says proposed but docs/adr/README.md marks it accepted",
		"docs/adr/0004-overmarked.md says accepted but docs/adr/README.md marks it proposed",
		"docs/adr/0008-unlisted.md is not listed in docs/adr/README.md",
	}
	if len(problems) != len(want) {
		t.Fatalf("validateDocumentation() problems = %v, want %v", problems, want)
	}
	for _, fragment := range want {
		if !containsFragment(problems, fragment) {
			t.Errorf("validateDocumentation() problems = %v, want one containing %q", problems, fragment)
		}
	}
}

func writeRequiredDocuments(t *testing.T, root string) {
	t.Helper()
	documents := map[string]string{}
	for _, name := range requiredDocuments {
		documents[name] = "# Test\n"
	}
	documents[currentDocument] = "# Test\n\n更新时间：2026-10-06\n"
	writeDocuments(t, root, documents)
}

func writeDocuments(t *testing.T, root string, documents map[string]string) {
	t.Helper()
	for name, content := range documents {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func containsFragment(problems []string, fragment string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, fragment) {
			return true
		}
	}
	return false
}
