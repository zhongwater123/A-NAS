package websearch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"go.yaml.in/yaml/v4"
)

// The release's SearXNG settings load every engine SearXNG ships except
// those that cannot answer without what A-NAS does not have: an API key or
// token, Tor, or a search service of one's own (docs/specs/web-search.md).
// The system test checks the engines SearXNG itself loads.
func TestTheReleaseLoadsEveryEngineThatCanAnswer(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "deploy", "search", "settings.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		UseDefaultSettings struct {
			Engines struct {
				Remove   []string `yaml:"remove"`
				KeepOnly []string `yaml:"keep_only"`
			} `yaml:"engines"`
		} `yaml:"use_default_settings"`
		Engines []struct {
			Name     string `yaml:"name"`
			Inactive *bool  `yaml:"inactive"`
		} `yaml:"engines"`
		Plugins map[string]any `yaml:"plugins"`
	}
	if err := yaml.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	removed := slices.Sorted(slices.Values(settings.UseDefaultSettings.Engines.Remove))
	want := []string{
		"Torznab EZTV", "ahmia", "astrophysics data system", "azure", "braveapi", "cloudflareai", "core.ac.uk", "deepl",
		"elasticsearch", "exaapi", "flickr_api", "freesound", "github code", "jina", "marginalia", "springer nature",
		"torch", "wolframalpha_api", "yandex api", "youtube_api",
	}
	if !slices.Equal(removed, want) || settings.UseDefaultSettings.Engines.KeepOnly != nil {
		t.Fatalf("removed = %v, keep_only = %v", removed, settings.UseDefaultSettings.Engines.KeepOnly)
	}
	// SearXNG ships some engines inactive: the settings turn them on.
	for _, engine := range settings.Engines {
		if engine.Inactive == nil || *engine.Inactive || slices.Contains(removed, engine.Name) {
			t.Errorf("engine %q is not turned on", engine.Name)
		}
	}
	// The plugins listed replace SearXNG's own; this one downloads its rules
	// at every start.
	if _, ok := settings.Plugins["searx.plugins.tracker_url_remover.SXNGPlugin"]; ok || len(settings.Plugins) == 0 {
		t.Fatalf("plugins = %v", settings.Plugins)
	}
}
