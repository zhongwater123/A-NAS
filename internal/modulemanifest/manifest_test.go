package modulemanifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/modulemanifest"
)

func TestManifestsNameValidModulesInTheirRange(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"scoring.json":  `{"id":"scoring","uid":31100}`,
		"caption.json":  `{"id":"caption","uid":31100}`,
		"other.json":    `{"id":"scoring2","uid":31101}`,
		"family.json":   `{"id":"family","uid":31002}`,
		"Bad.json":      `{"id":"Bad","uid":31102}`,
		"extra.json":    `{"id":"extra","uid":31103,"root":true}`,
		"notes.txt":     `ignored`,
		"labeling.json": `{"id":"labeling","uid":31104}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifests, err := modulemanifest.Read(dir)
	var ids []string
	for _, manifest := range manifests {
		ids = append(ids, manifest.ID)
	}
	// caption sorts before scoring and takes UID 31100 first.
	if strings.Join(ids, ",") != "caption,labeling" {
		t.Fatalf("manifests %v", ids)
	}
	for _, problem := range []string{"scoring.json", "other.json", "family.json", "Bad.json", "extra.json"} {
		if err == nil || !strings.Contains(err.Error(), problem) {
			t.Errorf("no problem reported for %s: %v", problem, err)
		}
	}
	if manifests, err := modulemanifest.Read(filepath.Join(dir, "missing")); manifests != nil || err != nil {
		t.Fatalf("a missing directory: %v, %v", manifests, err)
	}
}
