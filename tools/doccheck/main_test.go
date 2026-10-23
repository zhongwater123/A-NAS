package main

import (
	"os"
	"path/filepath"
	"slices"
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
	header := "# 当前状态\n\n更新时间：2026-10-23\n\n"
	for size, wantProblem := range map[int]bool{currentMaxBytes: false, currentMaxBytes + 1: true} {
		root := t.TempDir()
		writeRequiredDocuments(t, root)
		writeDocuments(t, root, map[string]string{currentDocument: header + strings.Repeat("x", size-len(header))})

		var want []string
		if wantProblem {
			want = []string{currentDocument + " is 12289 bytes, over the 12288-byte cap"}
		}
		assertProblems(t, validateDocumentationProblems(root), want)
	}
}

func TestValidateDocumentationKeepsMergeAndDeployStatusInCurrent(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		currentDocument: "# 当前状态\n\n更新时间：2026-10-23\n\n## 已合并尚未部署\n\n- #1\n",
		"docs/runbooks/accept-features-on-experimental-nas.md": "# 验收\n\n待合并的修复部署后再验收。\n",
		"docs/specs/music.md":           "# 音乐\n\n状态：implemented（尚未合并）\n",
		"docs/adr/0023-share-copies.md": "# 0023\n\n状态：proposed（待合并评审，尚未部署）\n",
		"docs/adr/README.md":            "- [0023：分享](0023-share-copies.md)（proposed）\n",
		"docs/research/upstream.md":     "上游 PR 在 2026-10-16 查看时仍开着。\n",
		"docs/specs/photo-people.md": "# 人物\n\n" +
			"相册列出待合并的人物建议。<!-- doccheck: not a status -->\n" +
			"部署进度见[当前状态](../status/CURRENT.md#已合并尚未部署)。\n",
		"docs/investigations/2026-10-21-snapshots.md": "# 快照\n\n状态：fixed（修复未部署）\n\n## 时间线\n\n" +
			"- Docker 与容器代理从未部署。\n" +
			"- 项目状态一直标记“Experimental NAS 未部署”（见旧版 README）。\n",
		"docs/architecture/photo-library.md": "13. 人物与宠物（已合并，未部署）。\n" +
			"14. 视频（[进度](../status/CURRENT.md)）当时未部署。\n",
	})

	assertProblems(t, validateDocumentationProblems(root), []string{
		"docs/specs/music.md:3 says 未合并",
		"docs/adr/0023-share-copies.md:3 says 待合并",
		"docs/investigations/2026-10-21-snapshots.md:3 says 未部署",
		"docs/architecture/photo-library.md:1 says 未部署",
	})
}

func TestValidateDocumentationMatchesADRStatusWithIndex(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		"docs/adr/0001-accepted.md":          "# 0001\n\n状态：accepted（2026-10-10）\n",
		"docs/adr/0002-proposed.md":          "# 0002\n\n状态：proposed（2026-10-12，随 PR 提交评审）\n",
		"docs/adr/0003-unmarked.md":          "# 0003\n\n状态：proposed\n",
		"docs/adr/0004-overmarked.md":        "# 0004\n\n状态：accepted\n",
		"docs/adr/0005-superseded.md":        "# 0005\n\n状态：superseded by [0006](0006-no-status.md)\n",
		"docs/adr/0006-no-status.md":         "# 0006\n\nThe decision.\n",
		"docs/adr/0007-amended.md":           "# 0007\n\nStatus: amended by [0001](0001-accepted.md).\n",
		"docs/adr/0008-unlisted.md":          "# 0008\n\n状态：accepted\n",
		"docs/adr/0009-english-status.md":    "# 0009\n\nStatus: Proposed\n",
		"docs/adr/0010-dot.md":               "# 0010\n\n状态：proposed\n",
		"docs/adr/0011-fragment.md":          "# 0011\n\n状态：accepted\n",
		"docs/adr/0012-angle.md":             "# 0012\n\n状态：proposed\n",
		"docs/adr/0013-prose-marker.md":      "# 0013\n\n状态：proposed\n",
		"docs/adr/0014-mentions-proposed.md": "# 0014\n\n状态：accepted\n",
		"docs/adr/0015-chinese-status.md":    "# 0015\n\n状态：提议中\n",
		"docs/adr/0016-bold-status.md":       "# 0016\n\n**状态**：proposed\n",
		"docs/adr/README.md": "# ADR\n\n" +
			"- [0001：已接受](0001-accepted.md)（修订 0007）\n" +
			"- [0002：提议](0002-proposed.md)（proposed）\n" +
			"- [0003：漏标](0003-unmarked.md)\n" +
			"- [0004：多标](0004-overmarked.md)（proposed）\n" +
			"- [0005：已替代](0005-superseded.md)（superseded，见 0006）\n" +
			"- [0006：无状态](0006-no-status.md)\n" +
			"- [0007：已修订](0007-amended.md)（已被 0001 修订）\n" +
			"- [0009：英文状态](0009-english-status.md)（proposed）\n" +
			"- [0010：相对路径](./0010-dot.md)（proposed）\n" +
			"- [0011：锚点](0011-fragment.md#决定)\n" +
			"- [0012：尖括号](<0012-angle.md>)（proposed）\n" +
			"- [0013：标记带说明](0013-prose-marker.md)（proposed；取代 0005 中已 superseded 的段落）\n" +
			"- [0014：提到别的状态](0014-mentions-proposed.md)（取代 0002 的 proposed 方案）\n" +
			"- [0015：中文状态](0015-chinese-status.md)\n" +
			"- [0016：加粗状态](0016-bold-status.md)（proposed）\n" +
			"- [0001：重复](0001-accepted.md)（proposed）\n" +
			"\n0002 的背景见 [0002](0002-proposed.md)。\n",
	})

	assertProblems(t, validateDocumentationProblems(root), []string{
		"docs/adr/0003-unmarked.md says proposed but docs/adr/README.md marks it accepted",
		"docs/adr/0004-overmarked.md says accepted but docs/adr/README.md marks it proposed",
		"docs/adr/0008-unlisted.md is not listed in docs/adr/README.md",
		"docs/adr/0015-chinese-status.md: start the 状态 line with proposed, accepted or superseded",
		"docs/adr/README.md lists 0001-accepted.md more than once",
	})
}

func validateDocumentationProblems(root string) []string {
	problems, _ := validateDocumentation(root)
	return problems
}

func assertProblems(t *testing.T, problems, want []string) {
	t.Helper()
	if len(problems) != len(want) {
		t.Fatalf("validateDocumentation() problems = %v, want %v", problems, want)
	}
	for _, fragment := range want {
		if !slices.ContainsFunc(problems, func(problem string) bool { return strings.Contains(problem, fragment) }) {
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
