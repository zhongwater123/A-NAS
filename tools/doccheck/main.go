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

	investigationDirectory = "docs/investigations"
	investigationIndex     = "docs/investigations/README.md"
	acceptanceRunbook      = "docs/runbooks/accept-features-on-experimental-nas.md"

	decisionDirectory = "docs/decisions"
	decisionIndex     = "docs/decisions/README.md"
	// The section of the current state that lists what waits for the user:
	// links to pending decision records and proposed ADRs.
	waitingHeading = "## 待用户决定"

	// Every session that continues earlier work reads CURRENT.md first
	// (AGENTS.md). It had grown to 67 KB of merge and deploy history before
	// #227 cut it to about 6 KB of current facts (#191), then grew back to
	// 12 KB, mostly the story of decisions already made. Those moved to
	// decision records, and the page became an index of about 8 KB. 10 KiB
	// leaves room for a crowded week of open work, but fails long before the
	// page turns back into a journal that every session must read and every
	// parallel branch conflicts on.
	currentMaxBytes = 10 << 10

	// The byte cap alone did not keep the page to current facts: for a week
	// it sat within 200 bytes of the cap while each pull request cut another
	// one's sentences to fit its own. Its 下一步 kept finished steps with
	// "已部署" added, and the evidence of each deploy had no other place to
	// go. So a next step says only what is left to do, in a line or two, and
	// the page as it stood before each deploy is kept as a dated snapshot
	// beside it.
	statusDirectory  = "docs/status"
	nextStepsHeading = "## 下一步"
	nextStepMaxRunes = 120
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
		decisionIndex,
		"docs/decisions/_TEMPLATE.md",
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
	statusPhraseKeepers = map[string]bool{currentDocument: true, acceptanceRunbook: true}
	// A document's 状态 line; the first group is the English status word it
	// starts with, if any, and the second the rest of the line.
	statusLine = regexp.MustCompile(`^\**(?:状态|Status)\**[：:][ \t]*([A-Za-z]*)(.*)$`)
	// A line that uses a phrase in its ordinary sense, such as merging two
	// people in the photo library, carries this comment.
	statusPhraseAllowed = "<!-- doccheck: not a status -->"
	linkDestination     = regexp.MustCompile(`\]\([^)]*\)`)
	linkTarget          = regexp.MustCompile(`\]\(([^)]+)\)`)

	// The current state names the commit the Experimental NAS runs on one
	// line, which the deploy rewrites. A hand-kept list of what merged since
	// went stale with every merge (#191); scripts/undeployed.sh lists it
	// from Git instead.
	deployedPrefix    = "- 已部署："
	deployedLine      = regexp.MustCompile("^- 已部署：`[0-9a-f]{7,40}`")
	pullRequestNumber = regexp.MustCompile(`#\d+|/pull/\d+`)

	// A snapshot is docs/status/<install date>.md, with -2 and so on for a
	// second deploy on the same day. It keeps the page's own 已部署 line and
	// names the commit deployed after it, so the snapshots and the current
	// state form one chain of deploys that this tool can check without Git.
	snapshotFileName = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}(-\d+)?\.md$`)
	deployedCommit   = regexp.MustCompile("(?m)^- 已部署：`([0-9a-f]{7,40})`")
	nextDeployPrefix = "下一次部署："
	nextDeployCommit = regexp.MustCompile("(?m)^下一次部署：`([0-9a-f]{7,40})`")
	currentLink      = "](CURRENT.md)"

	listItem     = regexp.MustCompile(`^\s*(?:[-*]|\d+\.)\s+`)
	markdownLink = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	// "已部署" and "已随 `<commit>` 部署": a next step that reports a deploy.
	deployedPhrase = regexp.MustCompile(`已(?:随[^，。；]*)?部署`)

	// The index lists each ADR as "- [NNNN：title](file)", followed by a
	// parenthesised marker that starts with proposed or superseded when the
	// ADR is not accepted.
	adrFileName    = regexp.MustCompile(`^\d{4}-.+\.md$`)
	adrIndexEntry  = regexp.MustCompile(`^\s*[-*]\s+\[\d{4}[^\]]*\]\(([^)]+)\)(.*)$`)
	adrIndexMarker = regexp.MustCompile(`^\s*[（(]\s*([A-Za-z]+)`)
	// An ADR still on a branch holds its number in the index with a line
	// that names it without a link, "- NNNN：title（draft，PR #N）", so that
	// parallel branches do not take the same number (#191).
	adrPlaceholder = regexp.MustCompile(`^\s*[-*]\s+(\d{4})[：:]`)
	adrStatuses    = map[string]bool{"proposed": true, "accepted": true, "superseded": true}

	// An investigation's status lives only on its own 状态 line; the index
	// once kept a second copy that drifted from it (#191). fixed means the
	// fix has merged and waits for the acceptance runbook's check on the
	// Experimental NAS, so that runbook must link the investigation.
	datedFileName            = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-.+\.md$`)
	investigationStatuses    = map[string]bool{"open": true, "fixed": true, "resolved": true, "superseded": true}
	indexEntry               = regexp.MustCompile(`^\s*[-*]\s+\[[^\]]*\]\(([^)]+)\)(.*)$`)
	investigationIndexStatus = regexp.MustCompile(`^\s*[（(]\s*(?i:open|fixed|resolved|superseded)\b`)

	// A decision the user made, or one that waits for the user, lives in one
	// decision record unless it is hard to reverse, which makes it an ADR.
	// Its status lives only on its own 状态 line, as an investigation's
	// does. A decided record names the day, so that looking back finds when
	// it changed. The index marks a pending record （pending）, as the ADR
	// index marks a proposed ADR, so that nothing waits for the user unseen,
	// as proposed ADRs did for weeks. The current state used to link each of
	// them by hand: that list took a quarter of the page and grew with every
	// open question, so the page now links the two indexes instead.
	decisionStatuses    = map[string]bool{"pending": true, "decided": true, "superseded": true}
	decisionIndexStatus = regexp.MustCompile(`^\s*[（(]\s*((?i:pending|decided|superseded))\b`)
	isoDate             = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)
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
	if err == nil {
		problems = append(problems, validateDeployedState(string(current))...)
		problems = append(problems, validateNextSteps(string(current))...)
	}

	files := 0
	adrDocuments, investigations, decisions, runbook := map[string]string{}, map[string]string{}, map[string]string{}, ""
	snapshots := map[string]string{}
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
		// A snapshot is an old current-state page, status phrases included.
		snapshot := path.Dir(relativePath) == statusDirectory && snapshotFileName.MatchString(path.Base(relativePath))
		if !statusPhraseKeepers[relativePath] && !snapshot {
			problems = append(problems, findStatusPhrases(relativePath, string(data))...)
		}
		switch {
		case snapshot:
			snapshots[relativePath] = string(data)
		case path.Dir(relativePath) == adrDirectory:
			adrDocuments[relativePath] = string(data)
		case path.Dir(relativePath) == investigationDirectory:
			investigations[relativePath] = string(data)
		case path.Dir(relativePath) == decisionDirectory:
			decisions[relativePath] = string(data)
		case relativePath == acceptanceRunbook:
			runbook = string(data)
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}

	problems = append(problems, validateADRIndex(adrDocuments)...)
	problems = append(problems, validateADRNumbers(adrDocuments)...)
	problems = append(problems, validateInvestigations(investigations, runbook)...)
	problems = append(problems, validateDecisions(decisions, string(current))...)
	problems = append(problems, validateSnapshots(snapshots, string(current))...)
	return problems, files
}

// validateNextSteps checks that each item under the current state's 下一步
// says what is left to do: it reports no deploy, and without its link targets
// it fits in nextStepMaxRunes characters.
func validateNextSteps(current string) []string {
	var problems []string
	inside := false
	for number, line := range strings.Split(current, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			inside = strings.TrimSpace(line) == nextStepsHeading
			continue
		}
		marker := listItem.FindString(line)
		if !inside || marker == "" {
			continue
		}
		item := line[len(marker):]
		// A link's text may name a document, such as 已部署功能的实机验收手册.
		if phrase := deployedPhrase.FindString(markdownLink.ReplaceAllString(item, "")); phrase != "" && !strings.Contains(line, statusPhraseAllowed) {
			problems = append(problems, fmt.Sprintf("%s:%d: a next step says %s: what runs goes under the Experimental NAS heading, and a finished step leaves the list", currentDocument, number+1, phrase))
		}
		if length := len([]rune(markdownLink.ReplaceAllString(item, "$1"))); length > nextStepMaxRunes {
			problems = append(problems, fmt.Sprintf("%s:%d: a next step is %d characters without its link targets, over %d: say what is left to do and link the rest", currentDocument, number+1, length, nextStepMaxRunes))
		}
	}
	return problems
}

// validateSnapshots checks that each snapshot is marked as one, and that the
// snapshots in date order and then the current state form a chain: each
// names the commit deployed after it, which the next one records as deployed.
// A deploy that rewrites the current state's 已部署 line without saving the
// page first breaks the chain at its end. snapshots maps the slash-separated
// paths of the snapshots to their contents; current is the current state's.
func validateSnapshots(snapshots map[string]string, current string) []string {
	var problems []string
	names := slices.SortedFunc(maps.Keys(snapshots), func(a, b string) int {
		// Without the extension, 2026-11-10 sorts before 2026-11-10-2.
		return strings.Compare(strings.TrimSuffix(a, ".md"), strings.TrimSuffix(b, ".md"))
	})
	previous, expected := "", ""
	for _, name := range names {
		content := snapshots[name]
		deployed, next := deployedCommit.FindAllStringSubmatch(content, -1), nextDeployCommit.FindAllStringSubmatch(content, -1)
		switch {
		case len(deployed) != 1:
			problems = append(problems, fmt.Sprintf("%s needs exactly one line starting with %s`<commit>`, found %d", name, deployedPrefix, len(deployed)))
			continue
		case len(next) != 1:
			problems = append(problems, fmt.Sprintf("%s needs exactly one line starting with %s`<commit>`, the commit deployed after it, found %d", name, nextDeployPrefix, len(next)))
			continue
		case !strings.Contains(content, currentLink):
			problems = append(problems, fmt.Sprintf("%s: say that it is a snapshot and link %s for the present state", name, currentDocument))
		}
		if previous != "" && !sameCommit(expected, deployed[0][1]) {
			problems = append(problems, fmt.Sprintf("%s says the next deploy was %s, but %s records %s as deployed", previous, expected, name, deployed[0][1]))
		}
		previous, expected = name, next[0][1]
	}
	if match := deployedCommit.FindStringSubmatch(current); previous != "" && match != nil && !sameCommit(expected, match[1]) {
		problems = append(problems, fmt.Sprintf("%s names %s as deployed, but the newest snapshot %s says the next deploy was %s: before rewriting the 已部署 line, save the page as %s/<install date>.md", currentDocument, match[1], previous, expected, statusDirectory))
	}
	return problems
}

// sameCommit reports whether two abbreviated commit names can be one commit.
func sameCommit(a, b string) bool {
	return strings.HasPrefix(a, b) || strings.HasPrefix(b, a)
}

// validateDecisions checks that the index lists every decision record and
// marks exactly the pending ones （pending）, that each 状态 line starts with
// pending, decided (with its date) or superseded, and that the current
// state's 待用户决定 links no single decision record or ADR. decisions maps
// the slash-separated paths of the Markdown files in docs/decisions to their
// contents; current is the current state's.
func validateDecisions(decisions map[string]string, current string) []string {
	var problems []string
	index, indexed := decisions[decisionIndex]
	listed, marked := map[string]bool{}, map[string]bool{}
	for line := range strings.SplitSeq(index, "\n") {
		match := indexEntry.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := path.Clean(normalizeLinkTarget(match[1]))
		if !datedFileName.MatchString(name) {
			continue
		}
		listed[name] = true
		switch status := decisionIndexStatus.FindStringSubmatch(match[2]); {
		case status == nil:
		case strings.EqualFold(status[1], "pending"):
			marked[name] = true
		default:
			problems = append(problems, fmt.Sprintf("%s marks %s %s: the index marks only pending, other statuses stay on the decision's 状态 line", decisionIndex, name, strings.ToLower(status[1])))
		}
	}

	for _, name := range slices.Sorted(maps.Keys(decisions)) {
		base := path.Base(name)
		if !datedFileName.MatchString(base) {
			continue
		}
		if indexed && !listed[base] {
			problems = append(problems, fmt.Sprintf("%s is not listed in %s", name, decisionIndex))
		}
		match, _ := headerStatus(decisions[name])
		status := ""
		if match != nil {
			status = strings.ToLower(match[1])
		}
		switch {
		case !decisionStatuses[status]:
			problems = append(problems, fmt.Sprintf("%s: start the 状态 line under the title with pending, decided or superseded", name))
		case status == "decided" && !isoDate.MatchString(match[2]):
			problems = append(problems, fmt.Sprintf("%s: give the day it was decided on the 状态 line, as in decided（2026-11-03，用户）", name))
		case status == "pending" && listed[base] && !marked[base]:
			problems = append(problems, fmt.Sprintf("%s is pending, but %s does not mark it （pending）", name, decisionIndex))
		case status != "pending" && marked[base]:
			problems = append(problems, fmt.Sprintf("%s is %s, but %s still marks it （pending）", name, status, decisionIndex))
		}
	}

	for _, name := range slices.Sorted(maps.Keys(waitingLinks(current))) {
		base := path.Base(name)
		if (path.Dir(name) == decisionDirectory && datedFileName.MatchString(base)) || (path.Dir(name) == adrDirectory && adrFileName.MatchString(base)) {
			problems = append(problems, fmt.Sprintf("%s links %s under %s: the indexes mark what waits for the user, so link %s and %s there instead", currentDocument, name, waitingHeading, decisionIndex, adrIndex))
		}
	}
	return problems
}

// waitingLinks returns the repository paths of the documents that the current
// state links under its 待用户决定 heading.
func waitingLinks(current string) map[string]bool {
	links := map[string]bool{}
	inside := false
	for line := range strings.SplitSeq(current, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			inside = line == waitingHeading
			continue
		}
		if !inside {
			continue
		}
		for _, match := range linkTarget.FindAllStringSubmatch(line, -1) {
			target := normalizeLinkTarget(match[1])
			if target == "" || strings.Contains(target, "://") {
				continue
			}
			links[path.Join(path.Dir(currentDocument), target)] = true
		}
	}
	return links
}

// validateInvestigations checks that the index lists every investigation
// without a status, that each 状态 line starts with a known status, and that
// the acceptance runbook links every fixed one. documents maps the
// slash-separated paths of the Markdown files in docs/investigations to their
// contents; runbook is the acceptance runbook's.
func validateInvestigations(documents map[string]string, runbook string) []string {
	var problems []string
	index, indexed := documents[investigationIndex]
	listed := map[string]bool{}
	for line := range strings.SplitSeq(index, "\n") {
		match := indexEntry.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := path.Clean(normalizeLinkTarget(match[1]))
		if !datedFileName.MatchString(name) {
			continue
		}
		listed[name] = true
		if investigationIndexStatus.MatchString(match[2]) {
			problems = append(problems, fmt.Sprintf("%s gives %s a status: keep it only on the investigation's 状态 line", investigationIndex, name))
		}
	}

	for _, name := range slices.Sorted(maps.Keys(documents)) {
		base := path.Base(name)
		if !datedFileName.MatchString(base) {
			continue
		}
		if indexed && !listed[base] {
			problems = append(problems, fmt.Sprintf("%s is not listed in %s", name, investigationIndex))
		}
		match, _ := headerStatus(documents[name])
		status := ""
		if match != nil {
			status = strings.ToLower(match[1])
		}
		switch {
		case !investigationStatuses[status]:
			problems = append(problems, fmt.Sprintf("%s: start the 状态 line under the title with open, fixed, resolved or superseded", name))
		case status == "fixed" && !strings.Contains(runbook, "investigations/"+base):
			problems = append(problems, fmt.Sprintf("%s is fixed, but %s has no check that links it: add one, or mark it resolved and say why the Experimental NAS has nothing to add", name, acceptanceRunbook))
		}
	}
	return problems
}

// validateDeployedState checks that the current state names one deployed
// commit and lists no undeployed pull requests by hand.
func validateDeployedState(current string) []string {
	var problems []string
	deployed := 0
	for number, line := range strings.Split(current, "\n") {
		if strings.HasPrefix(line, deployedPrefix) {
			deployed++
			if !deployedLine.MatchString(line) {
				problems = append(problems, fmt.Sprintf("%s:%d: write the deployed commit as %s`<commit>`", currentDocument, number+1, deployedPrefix))
			}
		}
		if strings.Contains(line, "未部署") && pullRequestNumber.MatchString(line) && !strings.Contains(line, statusPhraseAllowed) {
			problems = append(problems, fmt.Sprintf("%s:%d lists undeployed pull requests by hand: scripts/undeployed.sh lists them from Git", currentDocument, number+1))
		}
	}
	if deployed != 1 {
		problems = append(problems, fmt.Sprintf("%s needs exactly one line starting with %s`<commit>`, found %d", currentDocument, deployedPrefix, deployed))
	}
	return problems
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
		match, misplaced := headerStatus(documents[name])
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

// validateADRNumbers checks that no two ADRs share a number, counting the
// numbers the index holds for ADRs still on a branch, and that a held number
// is released once its ADR arrives. documents is as for validateADRIndex.
func validateADRNumbers(documents map[string]string) []string {
	var problems []string
	files := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(documents)) {
		if !adrFileName.MatchString(path.Base(name)) {
			continue
		}
		number := path.Base(name)[:4]
		if other, taken := files[number]; taken {
			problems = append(problems, fmt.Sprintf("%s and %s share the number %s", other, name, number))
			continue
		}
		files[number] = name
	}

	held := map[string]bool{}
	for line := range strings.SplitSeq(documents[adrIndex], "\n") {
		match := adrPlaceholder.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		number := match[1]
		switch name, arrived := files[number]; {
		case held[number]:
			problems = append(problems, fmt.Sprintf("%s holds %s more than once", adrIndex, number))
		case arrived:
			problems = append(problems, fmt.Sprintf("%s still holds %s for a branch, but %s has it: replace the placeholder with the entry", adrIndex, number, name))
		}
		held[number] = true
	}
	return problems
}

// headerStatus matches the 状态 line against the first line under a document's
// title, where it goes. misplaced reports a 状态 line further down but before
// the first section, which would otherwise escape the index check; one inside
// a section belongs to the body.
func headerStatus(content string) (match []string, misplaced bool) {
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
