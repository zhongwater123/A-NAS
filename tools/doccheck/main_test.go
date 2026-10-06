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
	for _, name := range requiredDocuments {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		content := "# Test\n"
		if name == "docs/status/CURRENT.md" {
			content += "\n更新时间：2026-10-06\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	broken := filepath.Join(root, "docs", "broken.md")
	if err := os.WriteFile(broken, []byte("[missing](missing.md)\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, files := validateDocumentation(root)
	if files != len(requiredDocuments)+1 {
		t.Fatalf("validateDocumentation() counted %d Markdown files, want %d", files, len(requiredDocuments)+1)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "missing.md") {
		t.Fatalf("validateDocumentation() problems = %v, want one missing-link problem", problems)
	}
}
