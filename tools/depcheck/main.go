// Command depcheck checks that Dependabot never puts a 0.x web package in a
// grouped pull request. Below 1.0 a "minor" release may change the API, and
// one such update in the group held back all the others (#232). Such a
// package has to be left out of every group, so that it gets a pull request
// of its own, or be ignored for version updates.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"
)

const (
	dependabotConfig = ".github/dependabot.yml"
	webPackage       = "web/package.json"
	webDirectory     = "/web"
)

var allUpdateTypes = []string{"version-update:semver-major", "version-update:semver-minor", "version-update:semver-patch"}

type config struct {
	Updates []update `yaml:"updates"`
}

type update struct {
	Ecosystem string           `yaml:"package-ecosystem"`
	Directory string           `yaml:"directory"`
	Ignore    []ignoreRule     `yaml:"ignore"`
	Groups    map[string]group `yaml:"groups"`
}

type ignoreRule struct {
	DependencyName string   `yaml:"dependency-name"`
	Versions       any      `yaml:"versions"`
	UpdateTypes    []string `yaml:"update-types"`
}

type group struct {
	Patterns        []string `yaml:"patterns"`
	ExcludePatterns []string `yaml:"exclude-patterns"`
}

type manifest struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func main() {
	problems, err := run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "depcheck:", err)
		os.Exit(1)
	}
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "depcheck:", problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
	fmt.Println("depcheck: ok")
}

// run checks the files of the repository it is started in.
func run() ([]string, error) {
	configData, err := os.ReadFile(dependabotConfig)
	if err != nil {
		return nil, err
	}
	packageData, err := os.ReadFile(webPackage)
	if err != nil {
		return nil, err
	}
	return check(configData, packageData)
}

// check returns a problem for each 0.x package in the manifest that
// Dependabot could update inside a group.
func check(configData, packageData []byte) ([]string, error) {
	var dependabot config
	if err := yaml.Unmarshal(configData, &dependabot); err != nil {
		return nil, fmt.Errorf("%s: %w", dependabotConfig, err)
	}
	var web manifest
	if err := json.Unmarshal(packageData, &web); err != nil {
		return nil, fmt.Errorf("%s: %w", webPackage, err)
	}
	index := slices.IndexFunc(dependabot.Updates, func(u update) bool {
		return u.Ecosystem == "npm" && u.Directory == webDirectory
	})
	if index < 0 {
		return nil, errors.New(dependabotConfig + " has no npm updates for " + webDirectory)
	}
	npm := dependabot.Updates[index]

	var problems []string
	for _, name := range zeroMajor(web) {
		if npm.ignored(name) {
			continue
		}
		var groups []string
		for groupName, g := range npm.Groups {
			if g.has(name) {
				groups = append(groups, groupName)
			}
		}
		if len(groups) > 0 {
			slices.Sort(groups)
			problems = append(problems, fmt.Sprintf("%s is 0.x but Dependabot groups it (%s); add it to the groups' exclude-patterns in %s",
				name, strings.Join(groups, ", "), dependabotConfig))
		}
	}
	return problems, nil
}

// zeroMajor lists the packages pinned below 1.0, sorted.
func zeroMajor(web manifest) []string {
	var names []string
	for _, dependencies := range []map[string]string{web.Dependencies, web.DevDependencies} {
		for name, version := range dependencies {
			if strings.HasPrefix(strings.TrimLeft(version, "^~=v"), "0.") {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// ignored reports whether no version update of the package can arrive: a
// rule names it without limiting the versions, and with every update type
// or none.
func (u update) ignored(name string) bool {
	for _, rule := range u.Ignore {
		if !matches(rule.DependencyName, name) || rule.Versions != nil {
			continue
		}
		every := true
		for _, updateType := range allUpdateTypes {
			every = every && slices.Contains(rule.UpdateTypes, updateType)
		}
		if len(rule.UpdateTypes) == 0 || every {
			return true
		}
	}
	return false
}

// has reports whether the group takes the package's updates. A group
// without patterns takes every package.
func (g group) has(name string) bool {
	in := len(g.Patterns) == 0 || slices.ContainsFunc(g.Patterns, func(pattern string) bool { return matches(pattern, name) })
	return in && !slices.ContainsFunc(g.ExcludePatterns, func(pattern string) bool { return matches(pattern, name) })
}

// matches reports whether a Dependabot pattern, where * stands for any run
// of characters, matches the whole name.
func matches(pattern, name string) bool {
	expression := "^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$"
	return regexp.MustCompile(expression).MatchString(name)
}
