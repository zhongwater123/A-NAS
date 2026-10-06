package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

	current, err := os.ReadFile(filepath.Join(root, "docs", "status", "CURRENT.md"))
	if err == nil && !updatedDate.Match(current) {
		problems = append(problems, "docs/status/CURRENT.md needs an 更新时间：YYYY-MM-DD line")
	}

	files := 0
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "build" || name == "node_modules" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}

		files++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range markdownLink.FindAllStringSubmatch(string(data), -1) {
			target := normalizeLinkTarget(match[1])
			if target == "" || isExternalLink(target) {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), filepath.FromSlash(target)))
			if _, statErr := os.Stat(resolved); statErr != nil {
				relativeFile, _ := filepath.Rel(root, path)
				problems = append(problems, fmt.Sprintf("%s links to missing %s", filepath.ToSlash(relativeFile), target))
			}
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}

	return problems, files
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
