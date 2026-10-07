package appstore

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"
)

// The catalog holds unmodified manifests and icons from CasaOS-AppStore at a
// pinned commit; see catalog/NOTICE.md for provenance and licensing.
//
//go:embed catalog
var embeddedCatalog embed.FS

// Entry is one catalog app with its original manifest and icon.
type Entry struct {
	App      App
	Compose  []byte
	Icon     []byte
	IconType string
}

// Catalog loads the embedded catalog.
func Catalog() ([]Entry, error) {
	root, err := fs.Sub(embeddedCatalog, "catalog")
	if err != nil {
		return nil, err
	}
	return LoadCatalog(root)
}

// LoadCatalog reads every <app-id>/docker-compose.yml under root.
func LoadCatalog(root fs.FS) ([]Entry, error) {
	directories, err := fs.ReadDir(root, ".")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, directory := range directories {
		if !directory.IsDir() {
			continue
		}
		id := directory.Name()
		if !ValidID(id) {
			return nil, fmt.Errorf("catalog app %q has an invalid ID", id)
		}
		compose, err := fs.ReadFile(root, path.Join(id, "docker-compose.yml"))
		if err != nil {
			return nil, fmt.Errorf("catalog app %s: %w", id, err)
		}
		app, err := parseMetadata(id, compose)
		if err != nil {
			return nil, fmt.Errorf("catalog app %s: %w", id, err)
		}
		entry := Entry{App: app, Compose: compose}
		for name, contentType := range map[string]string{"icon.png": "image/png", "icon.svg": "image/svg+xml"} {
			if icon, err := fs.ReadFile(root, path.Join(id, name)); err == nil {
				entry.Icon, entry.IconType = icon, contentType
			} else if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].App.ID < entries[j].App.ID })
	return entries, nil
}

// parseMetadata reads the top-level x-casaos block that describes the app.
func parseMetadata(id string, compose []byte) (App, error) {
	var document struct {
		CasaOS map[string]any `yaml:"x-casaos"`
	}
	if err := yaml.Unmarshal(compose, &document); err != nil {
		return App{}, err
	}
	meta := document.CasaOS
	if meta == nil {
		return App{}, errors.New("manifest has no x-casaos metadata")
	}
	app := App{
		ID:          id,
		Title:       localized(meta["title"]),
		Tagline:     localized(meta["tagline"]),
		Description: localized(meta["description"]),
		Category:    text(meta["category"]),
		Version:     text(meta["version"]),
		Author:      text(meta["author"]),
		Website:     text(meta["website"]),
		Scheme:      text(meta["scheme"]),
		Path:        text(meta["index"]),
	}
	if app.Title == "" {
		app.Title = id
	}
	if port, err := strconv.ParseUint(text(meta["port_map"]), 10, 16); err == nil {
		app.WebPort = uint16(port)
	}
	if app.Scheme == "" {
		app.Scheme = "http"
	}
	if app.Path == "" {
		app.Path = "/"
	}
	return app, nil
}

// localized prefers Simplified Chinese, then US English, then any language.
func localized(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		for _, language := range []string{"zh_CN", "en_US"} {
			if text := strings.TrimSpace(fmt.Sprint(typed[language])); typed[language] != nil && text != "" {
				return text
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if text := strings.TrimSpace(fmt.Sprint(typed[key])); text != "" {
				return text
			}
		}
	}
	return ""
}

func text(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
