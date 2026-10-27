package main

import (
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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
		currentDocument,
		"docs/architecture/OVERVIEW.md",
		adrIndex,
		"docs/specs/README.md",
		"docs/specs/_TEMPLATE.md",
		"docs/investigations/README.md",
		"docs/investigations/_TEMPLATE.md",
		"docs/runbooks/README.md",
		"docs/runbooks/_TEMPLATE.md",
	}
	updatedDate = regexp.MustCompile(`(?m)^更新时间：\d{4}-\d{2}-\d{2}$`)

	// Merge and deploy state goes stale as soon as a PR merges or deploys,
	// and nobody comes back to fix it (#191). Only the current-state page
	// and the acceptance runbook record it; other documents give the
	// implementation stage, or date what they observed.
	statusPhrases = []string{"未合并", "待合并", "尚未部署"}
	// History also says 未部署 ("Docker 从未部署", an old status quoted), so
	// it counts only where a document labels its current state: on a 状态
	// line, or in parentheses such as an index entry's (……，未部署).
	labelPhrases        = []string{"未部署"}
	statusPhraseKeepers = map[string]bool{currentDocument: true, "docs/runbooks/accept-features-on-experimental-nas.md": true}
	// A document's 状态 line; the group is the English status word it starts
	// with, if any.
	statusLine = regexp.MustCompile(`^\**(?:状态|Status)\**[：:][ \t]*([A-Za-z]*)`)
	// A line that uses a phrase in its ordinary sense, such as merging two
	// people in the photo library, carries this comment.
	statusPhraseAllowed = "<!-- doccheck: not a status -->"
	linkDestination     = regexp.MustCompile(`\]\([^)]*\)`)

	// The index lists each ADR as "- [NNNN：title](file)", followed by a
	// parenthesised marker that starts with proposed or superseded when the
	// ADR is not accepted.
	adrFileName    = regexp.MustCompile(`^\d{4}-.+\.md$`)
	adrIndexEntry  = regexp.MustCompile(`^\s*[-*]\s+\[\d{4}[^\]]*\]\(([^)]+)\)(.*)$`)
	adrIndexMarker = regexp.MustCompile(`^\s*[（(]\s*([A-Za-z]+)`)
	adrStatuses    = map[string]bool{"proposed": true, "accepted": true, "superseded": true}
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
	adrDocuments := map[string]string{}
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relativePath, _ := filepath.Rel(root, filePath)
		relativePath = filepath.ToSlash(relativePath)
		if entry.IsDir() {
			name := entry.Name()
			// Claude Code worktrees are other checkouts with their own docs.
			if name == ".git" || name == "build" || name == "node_modules" || name == "dist" || relativePath == ".claude/worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(filePath), ".md") {
			return nil
		}

		files++
		data, readErr := os.ReadFile(filePath) //nolint:gosec // G122: a developer tool reading the checkout's own documents.
		if readErr != nil {
			return readErr
		}
		if !statusPhraseKeepers[relativePath] {
			problems = append(problems, findStatusPhrases(relativePath, string(data))...)
		}
		if path.Dir(relativePath) == adrDirectory {
			adrDocuments[relativePath] = string(data)
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}

	problems = append(problems, validateADRIndex(adrDocuments)...)
	return problems, files
}

func findStatusPhrases(relativePath, content string) []string {
	var problems []string
	for number, line := range strings.Split(content, "\n") {
		if strings.Contains(line, statusPhraseAllowed) {
			continue
		}
		// A link to a heading in CURRENT.md may carry a phrase in its anchor.
		text := linkDestination.ReplaceAllString(line, "]")
		if phrase := statusPhrase(text); phrase != "" {
			problems = append(problems, fmt.Sprintf("%s:%d says %s: merge and deploy status belongs in %s", relativePath, number+1, phrase, currentDocument))
		}
	}
	return problems
}

// statusPhrase returns the first merge or deploy status that line states, or
// "" when it states none.
func statusPhrase(line string) string {
	for _, phrase := range statusPhrases {
		if strings.Contains(line, phrase) {
			return phrase
		}
	}
	for _, phrase := range labelPhrases {
		if labelsWith(line, phrase) {
			return phrase
		}
	}
	return ""
}

// labelsWith reports whether line uses phrase as a status label: anywhere on
// a 状态 line, or inside parentheses elsewhere.
func labelsWith(line, phrase string) bool {
	if statusLine.MatchString(strings.TrimSpace(line)) {
		return strings.Contains(line, phrase)
	}
	depth := 0
	for index, r := range line {
		switch {
		case r == '（' || r == '(':
			depth++
		case r == '）' || r == ')':
			depth = max(depth-1, 0)
		case depth > 0 && strings.HasPrefix(line[index:], phrase):
			return true
		}
	}
	return false
}

// validateADRIndex checks that every ADR is listed once in the index and that
// the index marks proposed and superseded ADRs as their own 状态 line says.
// documents maps the slash-separated paths of the Markdown files in
// docs/adr to their contents.
func validateADRIndex(documents map[string]string) []string {
	index, found := documents[adrIndex]
	if !found {
		return nil
	}
	var problems []string
	markers := map[string]string{}
	for line := range strings.SplitSeq(index, "\n") {
		match := adrIndexEntry.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := path.Clean(normalizeLinkTarget(match[1]))
		if strings.Contains(name, "/") || !adrFileName.MatchString(name) {
			continue
		}
		if _, seen := markers[name]; seen {
			problems = append(problems, fmt.Sprintf("%s lists %s more than once", adrIndex, name))
			continue
		}
		markers[name] = match[2]
	}

	for _, name := range slices.Sorted(maps.Keys(documents)) {
		if !adrFileName.MatchString(path.Base(name)) {
			continue
		}
		marker, listed := markers[path.Base(name)]
		if !listed {
			problems = append(problems, fmt.Sprintf("%s is not listed in %s", name, adrIndex))
			continue
		}
		match, misplaced := adrStatus(documents[name])
		if misplaced {
			problems = append(problems, fmt.Sprintf("%s: move the 状态 line directly under the title so the index can be checked", name))
			continue
		}
		if match == nil {
			continue
		}
		status := strings.ToLower(match[1])
		if status == "" {
			problems = append(problems, fmt.Sprintf("%s: start the 状态 line with proposed, accepted or superseded so the index can be checked", name))
			continue
		}
		if !adrStatuses[status] {
			continue
		}
		if indexed := indexedADRStatus(marker); indexed != status {
			problems = append(problems, fmt.Sprintf("%s says %s but %s marks it %s", name, status, adrIndex, indexed))
		}
	}
	return problems
}

// adrStatus matches the 状态 line against the first line under an ADR's
// title, where it goes. misplaced reports a 状态 line further down but before
// the first section, which would otherwise escape the index check; one inside
// a section belongs to the body.
func adrStatus(content string) (match []string, misplaced bool) {
	titled, header := false, true
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case !titled && strings.HasPrefix(line, "# "):
			titled = true
		case strings.HasPrefix(line, "## "):
			return nil, false
		case header:
			if match = statusLine.FindStringSubmatch(line); match != nil {
				return match, false
			}
			header = false
		case statusLine.MatchString(line):
			return nil, true
		}
	}
	return nil, false
}

// indexedADRStatus reads the status from the marker after an index link: only
// a leading proposed or superseded counts, so the marker may still mention
// other ADRs in prose.
func indexedADRStatus(marker string) string {
	if match := adrIndexMarker.FindStringSubmatch(marker); match != nil {
		if word := strings.ToLower(match[1]); word == "proposed" || word == "superseded" {
			return word
		}
	}
	return "accepted"
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
