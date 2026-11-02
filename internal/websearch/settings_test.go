package websearch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.yaml.in/yaml/v4"
)

// The release's SearXNG settings load exactly the engines of the table, and
// turn each on: an engine missing there answers nothing, and one missing here
// is loaded for no tab.
func TestTheReleaseLoadsEveryEngineOfTheTable(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "deploy", "search", "settings.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		UseDefaultSettings struct {
			Engines struct {
				KeepOnly []string `yaml:"keep_only"`
			} `yaml:"engines"`
		} `yaml:"use_default_settings"`
		Engines []struct {
			Name     string `yaml:"name"`
			Disabled bool   `yaml:"disabled"`
		} `yaml:"engines"`
		Plugins map[string]any `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	var table []string
	for _, category := range Categories {
		for _, e := range engines[category] {
			table = append(table, e.name)
		}
	}
	slices.Sort(table)
	kept := slices.Sorted(slices.Values(settings.UseDefaultSettings.Engines.KeepOnly))
	if !slices.Equal(kept, table) {
		t.Fatalf("keep_only = %v, the table has %v", kept, table)
	}
	var enabled []string
	for _, e := range settings.Engines {
		if !e.Disabled {
			enabled = append(enabled, e.Name)
		}
	}
	if slices.Sort(enabled); !slices.Equal(enabled, table) {
		t.Fatalf("turned on = %v, the table has %v", enabled, table)
	}
	// The plugins listed replace SearXNG's own; this one downloads its rules
	// at every start.
	if _, ok := settings.Plugins["searx.plugins.tracker_url_remover.SXNGPlugin"]; ok || len(settings.Plugins) == 0 {
		t.Fatalf("plugins = %v", settings.Plugins)
	}
}
