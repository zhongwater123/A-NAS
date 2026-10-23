package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	currentDocument = "docs/status/CURRENT.md"
	adrDirectory    = "docs/adr"
	adrIndex        = "docs/adr/README.md"

	// Every session that continues earlier work reads CURRENT.md first
	// (AGENTS.md). It had grown to 67 KB of merge and deploy history before
	// #227 cut it to about 6 KB of current facts (#191). 12 KiB is twice
	// that: room for a crowded week of open work, but it fails long before
	// the page turns back into a journal that every session must read and
	// every parallel branch conflicts on.
	currentMaxBytes = 12 << 10
)

var (
	requiredDocuments = []string{
		"AGENTS.md",
		"CONTEXT.md",
		"PROJECT_CONTEXT.md",
		"docs/README.md",
		"docs/status/CURRENT.md",
		"docs/architecture/OVERVIEW.md",
		"docs/adr/README.md",
		"docs/specs/README.md",
		"docs/specs/_TEMPLATE.md",
		"docs/investigations/README.md",
		"docs/investigations/_TEMPLATE.md",
		"docs/runbooks/README.md",
		"docs/runbooks/_TEMPLATE.md",
	}
	markdownLink = regexp.MustCompile(`!?\[[^]]*\]\(([^)]+)\)`)
	updatedDate  = regexp.MustCompile(`(?m)^更新时间：\d{4}-\d{2}-\d{2}$`)

	// Merge and deploy state goes stale as soon as a PR merges or deploys,
	// and nobody comes back to fix it (#191). Only the current-state page
	// and the acceptance runbook record it; other documents give the
	// implementation stage, or date what they observed.
	statusPhrases       = []string{"未合并", "待合并", "尚未部署"}
	statusPhraseKeepers = map[string]bool{currentDocument: true, "docs/runbooks/accept-features-on-experimental-nas.md": true}
	adrFileName         = regexp.MustCompile(`^\d{4}-.+\.md$`)
	adrStatusLine       = regexp.MustCompile(`(?m)^(?:状态[：:]|Status:)\s*([A-Za-z]+)`)
	adrIndexLink        = regexp.MustCompile(`\]\((\d{4}-[^)]+\.md)\)(.*)$`)
	comparableADRStatus = map[string]bool{"proposed": true, "accepted": true, "superseded": true}
)

func main() {
	root, err := findRepositoryRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "doccheck:", err)
		os.Exit(1)
	}

	problems, files := validateDocumentation(root)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "doccheck:", problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}

	fmt.Printf("doccheck: ok (%d Markdown files)\n", files)
}

func findRepositoryRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			resolved, err := filepath.EvalSymlinks(dir)
			if err != nil {
				return "", fmt.Errorf("resolve repository root: %w", err)
			}
			return resolved, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func validateDocumentation(root string) ([]string, int) {
	var problems []string
	for _, name := range requiredDocuments {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(name))); err != nil {
			problems = append(problems, fmt.Sprintf("required document %s is missing", name))
		}
	}

	current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(currentDocument)))
	if err == nil && !updatedDate.Match(current) {
		problems = append(problems, currentDocument+" needs an 更新时间：YYYY-MM-DD line")
	}
	if err == nil && len(current) > currentMaxBytes {
		problems = append(problems, fmt.Sprintf("%s is %d bytes, over the %d-byte cap: keep only current facts and next steps", currentDocument, len(current), currentMaxBytes))
	}

	files := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, _ := filepath.Rel(root, path)
		relativePath = filepath.ToSlash(relativePath)
		if entry.IsDir() {
			name := entry.Name()
			// Claude Code worktrees are other checkouts with their own docs.
			if name == ".git" || name == "build" || name == "node_modules" || name == "dist" || relativePath == ".claude/worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}

		files++
		data, readErr := os.ReadFile(path) //nolint:gosec // G122: a developer tool reading the checkout's own documents.
		if readErr != nil {
			return readErr
		}
		if !statusPhraseKeepers[relativePath] {
			problems = append(problems, findStatusPhrases(relativePath, string(data))...)
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
			target := normalizeLinkTarget(match[1])
			if target == "" || isExternalLink(target) {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
			if _, statErr := os.Stat(resolved); statErr != nil {
				problems = append(problems, fmt.Sprintf("%s links to missing %s", relativePath, target))
			}
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}

	problems = append(problems, validateADRIndex(root)...)
	return problems, files
}

func findStatusPhrases(relativePath, content string) []string {
	var problems []string
	for number, line := range strings.Split(content, "\n") {
		for _, phrase := range statusPhrases {
			if strings.Contains(line, phrase) {
				problems = append(problems, fmt.Sprintf("%s:%d says %s: merge and deploy status belongs in %s", relativePath, number+1, phrase, currentDocument))
				break
			}
		}
	}
	return problems
}

// validateADRIndex checks that every ADR is listed in the index and that the
// index marks proposed and superseded ADRs as their own 状态 line says.
// Accepted ADRs carry no marker. ADRs without a proposed, accepted or
// superseded status word are only checked for an index entry.
func validateADRIndex(root string) []string {
	index, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(adrIndex)))
	if err != nil {
		return nil
	}
	markers := map[string]string{}
	for line := range strings.SplitSeq(string(index), "\n") {
		if match := adrIndexLink.FindStringSubmatch(line); match != nil {
			markers[match[1]] = match[2]
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(adrDirectory)))
	if err != nil {
		return []string{err.Error()}
	}
	var problems []string
	for _, entry := range entries {
		if entry.IsDir() || !adrFileName.MatchString(entry.Name()) {
			continue
		}
		name := adrDirectory + "/" + entry.Name()
		marker, listed := markers[entry.Name()]
		if !listed {
			problems = append(problems, fmt.Sprintf("%s is not listed in %s", name, adrIndex))
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		match := adrStatusLine.FindSubmatch(data)
		if match == nil {
			continue
		}
		status := strings.ToLower(string(match[1]))
		if !comparableADRStatus[status] {
			continue
		}
		if indexed := indexedADRStatus(marker); indexed != status {
			problems = append(problems, fmt.Sprintf("%s says %s but %s marks it %s", name, status, adrIndex, indexed))
		}
	}
	return problems
}

func indexedADRStatus(marker string) string {
	marker = strings.ToLower(marker)
	switch {
	case strings.Contains(marker, "superseded"):
		return "superseded"
	case strings.Contains(marker, "proposed"):
		return "proposed"
	default:
		return "accepted"
	}
}

func normalizeLinkTarget(raw string) string {
	target := strings.TrimSpace(raw)
	if strings.HasPrefix(target, "<") {
		if end := strings.Index(target, ">"); end >= 0 {
			target = target[1:end]
		}
	} else if fields := strings.Fields(target); len(fields) > 0 {
		target = fields[0]
	}
	if before, _, found := strings.Cut(target, "#"); found {
		target = before
	}
	return target
}

func isExternalLink(target string) bool {
	return strings.HasPrefix(target, "#") ||
		strings.HasPrefix(target, "/") ||
		strings.Contains(target, "://") ||
		strings.HasPrefix(target, "mailto:")
}
