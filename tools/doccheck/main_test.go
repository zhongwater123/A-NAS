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

func TestValidateDocumentationSkipsDependenciesAndOtherCheckouts(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		"docs/specs/music.md":                           "# 音乐\n\n状态：implemented（尚未合并）\n",
		"web/node_modules/dependency/README.md":         "尚未部署\n",
		"internal/webui/dist/generated.md":              "尚未部署\n",
		".claude/worktrees/other-branch/docs/broken.md": "尚未部署\n",
	})

	problems, files := validateDocumentation(root)
	if files != len(requiredDocuments)+1 {
		t.Fatalf("validateDocumentation() counted %d Markdown files, want %d", files, len(requiredDocuments)+1)
	}
	assertProblems(t, problems, []string{"docs/specs/music.md:3 says 未合并"})
}

func TestValidateDocumentationCapsCurrentSize(t *testing.T) {
	header := "# 当前状态\n\n更新时间：2026-10-23\n\n- 已部署：`80b602275b86`\n\n"
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
		currentDocument:                 "# 当前状态\n\n更新时间：2026-10-23\n\n- 已部署：`80b602275b86`\n\n## 已合并尚未部署\n\n运行 scripts/undeployed.sh。\n",
		acceptanceRunbook:               "# 验收\n\n待合并的修复部署后再验收：[快照](../investigations/2026-10-21-snapshots.md)。\n",
		investigationIndex:              "- [2026-10-21：快照](2026-10-21-snapshots.md)\n",
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
		"docs/research/labels.md": "已发布，未部署的只有 beta (beta 未部署)。\n" +
			"（见（旧版）未部署）\n" +
			"Status: fixed, 未部署\n" +
			"（旧版）之后未部署，(另见（附录）) 也未部署。\n",
	})

	assertProblems(t, validateDocumentationProblems(root), []string{
		"docs/specs/music.md:3 says 未合并",
		"docs/adr/0023-share-copies.md:3 says 待合并",
		"docs/investigations/2026-10-21-snapshots.md:3 says 未部署",
		"docs/architecture/photo-library.md:1 says 未部署",
		"docs/research/labels.md:1 says 未部署",
		"docs/research/labels.md:2 says 未部署",
		"docs/research/labels.md:3 says 未部署",
	})
}

func TestValidateDocumentationNamesOneDeployedCommit(t *testing.T) {
	header := "# 当前状态\n\n更新时间：2026-10-27\n\n"
	tests := map[string]struct {
		body string
		want []string
	}{
		"one commit": {body: "- 已部署：`80b602275b86`（PR #250 的合并提交，2026-10-26 安装）\n- 已合并、尚未部署的 PR 由 scripts/undeployed.sh 列出。\n"},
		"none":       {body: "- 主干 `80b602275b86`\n", want: []string{"needs exactly one line starting with - 已部署：`<commit>`, found 0"}},
		"two": {
			body: "- 已部署：`80b602275b86`\n- 已部署：`bb1ec71bff65`\n",
			want: []string{"needs exactly one line starting with - 已部署：`<commit>`, found 2"},
		},
		"no commit": {body: "- 已部署：主干最新\n", want: []string{currentDocument + ":5: write the deployed commit as - 已部署：`<commit>`"}},
		"hand list": {
			body: "- 已部署：`80b602275b86`\n" +
				"- 已合并、尚未部署：[#245](https://github.com/zhongwater123/A-NAS/pull/245)，随下一次部署。\n" +
				"- #203 的语音处理合并后也不部署，缺密钥。\n" +
				"- 未部署的 #141 只是评估。<!-- doccheck: not a status -->\n",
			want: []string{currentDocument + ":6 lists undeployed pull requests by hand"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeRequiredDocuments(t, root)
			writeDocuments(t, root, map[string]string{currentDocument: header + test.body})
			assertProblems(t, validateDocumentationProblems(root), test.want)
		})
	}
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
		"docs/adr/0017-body-status.md":       "# 0017\n\n背景与决定。\n\n## 后果\n\n状态：accepted 的 ADR 不再改写。\n",
		"docs/adr/0018-late-status.md":       "# 0018\n\n背景与决定。\n\n状态：accepted\n\n## 后果\n",
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
			"- [0017：正文提到状态](0017-body-status.md)（proposed）\n" +
			"- [0018：状态行不在标题下](0018-late-status.md)（proposed）\n" +
			"- [0001：重复](0001-accepted.md)（proposed）\n" +
			"\n0002 的背景见 [0002](0002-proposed.md)。\n",
	})

	assertProblems(t, validateDocumentationProblems(root), []string{
		"docs/adr/0003-unmarked.md says proposed but docs/adr/README.md marks it accepted",
		"docs/adr/0004-overmarked.md says accepted but docs/adr/README.md marks it proposed",
		"docs/adr/0008-unlisted.md is not listed in docs/adr/README.md",
		"docs/adr/0015-chinese-status.md: start the 状态 line with proposed, accepted or superseded",
		"docs/adr/0018-late-status.md: move the 状态 line directly under the title",
		"docs/adr/README.md lists 0001-accepted.md more than once",
	})
}

func TestValidateDocumentationKeepsADRNumbersApart(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		"docs/adr/0001-first.md":   "# 0001\n\n状态：accepted\n",
		"docs/adr/0002-second.md":  "# 0002\n\n状态：accepted\n",
		"docs/adr/0002-clash.md":   "# 0002\n\n状态：accepted\n",
		"docs/adr/0003-arrived.md": "# 0003\n\n状态：proposed\n",
		"docs/adr/README.md": "# ADR\n\n" +
			"- [0001：第一](0001-first.md)\n" +
			"- [0002：第二](0002-second.md)\n" +
			"- [0002：撞号](0002-clash.md)\n" +
			"- [0003：已合并](0003-arrived.md)（proposed）\n" +
			"- 0003：已合并（draft，PR #3）\n" +
			"- 0004：分支上（draft，PR #4）\n" +
			"- 0005：另一个分支（draft，PR #5）\n" +
			"- 0005：同号的分支（draft，PR #6）\n" +
			"\n2026 年 0006 号之后再议。\n",
	})

	assertProblems(t, validateDocumentationProblems(root), []string{
		"docs/adr/0002-clash.md and docs/adr/0002-second.md share the number 0002",
		"docs/adr/README.md still holds 0003 for a branch, but docs/adr/0003-arrived.md has it",
		"docs/adr/README.md holds 0005 more than once",
	})
}

func TestValidateDocumentationKeepsInvestigationStatusInOnePlace(t *testing.T) {
	root := t.TempDir()
	writeRequiredDocuments(t, root)
	writeDocuments(t, root, map[string]string{
		"docs/investigations/2026-10-01-open.md":         "# 调查\n\n状态：open\n更新时间：2026-10-01\n",
		"docs/investigations/2026-10-02-checked.md":      "# 调查\n\n状态：fixed（PR #2），待实机验收\n",
		"docs/investigations/2026-10-03-unchecked.md":    "# 调查\n\n状态：fixed，待实机验收\n",
		"docs/investigations/2026-10-04-resolved.md":     "# 调查\n\n状态：resolved；回归测试即证据，实机无可补充\n",
		"docs/investigations/2026-10-05-superseded.md":   "# 调查\n\n状态：superseded by ADR 0008\n",
		"docs/investigations/2026-10-06-pending.md":      "# 调查\n\n状态：fixed locally；Experimental NAS verification pending\n",
		"docs/investigations/2026-10-07-fix-in-pr.md":    "# 调查\n\n状态：fix in PR #39\n",
		"docs/investigations/2026-10-08-no-status.md":    "# 调查\n\n症状。\n",
		"docs/investigations/2026-10-09-unlisted.md":     "# 调查\n\n状态：resolved\n",
		"docs/investigations/2026-10-10-index-status.md": "# 调查\n\n状态：resolved\n",
		"docs/investigations/_TEMPLATE.md":               "# <问题标题>\n\n状态：open\n",
		"docs/investigations/README.md": "# 调查记录\n\n" +
			"- [2026-10-01：开放](2026-10-01-open.md)\n" +
			"- [2026-10-02：已核对](2026-10-02-checked.md)\n" +
			"- [2026-10-03：未核对](2026-10-03-unchecked.md)\n" +
			"- [2026-10-04：已解决](2026-10-04-resolved.md)\n" +
			"- [2026-10-05：已替代](2026-10-05-superseded.md)\n" +
			"- [2026-10-06：待验收](2026-10-06-pending.md)\n" +
			"- [2026-10-07：旧写法](2026-10-07-fix-in-pr.md)\n" +
			"- [2026-10-08：无状态](2026-10-08-no-status.md)\n" +
			"- [2026-10-10：索引带状态](2026-10-10-index-status.md)（resolved）\n" +
			"\n从 [`_TEMPLATE.md`](_TEMPLATE.md) 创建。\n",
		acceptanceRunbook: "# 验收\n\n- 核对（[调查](../investigations/2026-10-02-checked.md)）。\n" +
			"- 核对（[调查](../investigations/2026-10-06-pending.md#后续工作)）。\n",
	})

	assertProblems(t, validateDocumentationProblems(root), []string{
		"docs/investigations/2026-10-03-unchecked.md is fixed, but " + acceptanceRunbook + " has no check that links it",
		"docs/investigations/2026-10-07-fix-in-pr.md: start the 状态 line under the title with open, fixed, resolved or superseded",
		"docs/investigations/2026-10-08-no-status.md: start the 状态 line",
		"docs/investigations/2026-10-09-unlisted.md is not listed in " + investigationIndex,
		investigationIndex + " gives 2026-10-10-index-status.md a status",
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
	documents[currentDocument] = "# Test\n\n更新时间：2026-10-06\n\n- 已部署：`80b602275b86`\n"
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
