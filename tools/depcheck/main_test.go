package main

import (
	"slices"
	"strings"
	"testing"
)

const webManifest = `{
  "dependencies": {"@assistant-ui/react": "0.14.24", "@xterm/addon-fit": "0.11.0", "react": "19.3.0"},
  "devDependencies": {"oxlint": "1.89.0", "dom-accessibility-api": "^0.7.1"}
}`

func configWith(npm string) []byte {
	return []byte(`version: 2
updates:
  - package-ecosystem: npm
    directory: /web
` + npm)
}

func TestZeroMajorPackagesStayOutOfGroups(t *testing.T) {
	for _, test := range []struct {
		name string
		npm  string
		want []string // packages reported
	}{
		{
			name: "excluded or ignored",
			npm: `    ignore:
      - dependency-name: "@assistant-ui/*"
        update-types: ["version-update:semver-major", "version-update:semver-minor", "version-update:semver-patch"]
    groups:
      lint:
        patterns: ["oxlint"]
      web:
        patterns: ["*"]
        exclude-patterns: ["@xterm/addon-fit", "dom-accessibility-api"]
`,
		},
		{
			name: "a catch-all group",
			npm: `    groups:
      web:
        patterns: ["*"]
        exclude-patterns: ["@xterm/addon-fit"]
`,
			want: []string{"@assistant-ui/react", "dom-accessibility-api"},
		},
		{
			name: "a group without patterns takes every package",
			npm: `    groups:
      web:
        update-types: [minor, patch]
`,
			want: []string{"@assistant-ui/react", "@xterm/addon-fit", "dom-accessibility-api"},
		},
		{
			name: "ignoring only minor updates still groups the patches",
			npm: `    ignore:
      - dependency-name: "@assistant-ui/*"
        update-types: ["version-update:semver-minor"]
      - dependency-name: "@xterm/addon-fit"
        versions: [">= 0.12.0"]
      - dependency-name: "dom-accessibility-api"
    groups:
      ui:
        patterns: ["@assistant-ui/*", "@xterm/*", "dom-*"]
`,
			want: []string{"@assistant-ui/react", "@xterm/addon-fit"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			problems, err := check(configWith(test.npm), []byte(webManifest))
			if err != nil {
				t.Fatal(err)
			}
			var reported []string
			for _, problem := range problems {
				reported = append(reported, strings.Fields(problem)[0])
			}
			if !slices.Equal(reported, test.want) {
				t.Fatalf("reported %v, want %v; problems: %q", reported, test.want, problems)
			}
		})
	}
}

func TestTheWebUpdatesMustBeConfigured(t *testing.T) {
	if _, err := check([]byte("version: 2\nupdates: []\n"), []byte(webManifest)); err == nil {
		t.Fatal("check() of a config without npm updates for /web succeeded")
	}
}

func TestPatternsMatchLikeDependabot(t *testing.T) {
	for _, test := range []struct {
		pattern, name string
		want          bool
	}{
		{"@assistant-ui/*", "@assistant-ui/react-streamdown", true},
		{"@assistant-ui/*", "assistant-ui", false},
		{"eslint-plugin-*", "eslint-plugin-react-hooks", true},
		{"*", "@types/node", true},
		{"react", "react-dom", false},
		{"@types/react.dom", "@types/react-dom", false},
	} {
		if got := matches(test.pattern, test.name); got != test.want {
			t.Errorf("matches(%q, %q) = %v, want %v", test.pattern, test.name, got, test.want)
		}
	}
}
