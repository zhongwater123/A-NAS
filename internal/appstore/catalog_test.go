package appstore_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

var testPolicy = appstore.Policy{
	AppDataRoot:   "/srv/a-nas/data/apps",
	DataRoot:      "/srv/a-nas/data/spaces/shared",
	TZ:            "Asia/Shanghai",
	ReservedPorts: []uint16{8080},
}

var testIdentity = appstore.Identity{Username: "app-demo", UID: 30005, GID: 30005}

// Every vendored manifest must render under the install policy, so the store
// never lists an app that would be refused at install time.
func TestEmbeddedCatalogPassesPolicy(t *testing.T) {
	entries, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("catalog is empty")
	}
	for _, entry := range entries {
		plan, err := appstore.Render(context.Background(), entry, testPolicy, testIdentity, secretsFor(entry.App.ID))
		if err != nil {
			if policyError, ok := errors.AsType[*appstore.PolicyError](err); ok {
				t.Errorf("%s: %s", entry.App.ID, strings.Join(policyError.Reasons, " | "))
				continue
			}
			t.Errorf("%s: %v", entry.App.ID, err)
			continue
		}
		if entry.Icon == nil || entry.App.Title == "" || len(plan.Images) == 0 {
			t.Errorf("%s: incomplete entry title=%q icon=%v images=%v", entry.App.ID, entry.App.Title, entry.Icon != nil, plan.Images)
		}
	}
}

// Every vendored app is listed under an A-NAS category, so none is only
// reachable through search.
func TestEveryCatalogAppHasACategory(t *testing.T) {
	entries, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !slices.Contains(appstore.Categories, entry.App.Category) {
			t.Errorf("%s: category %q is not an A-NAS category", entry.App.ID, entry.App.Category)
		}
	}
}

func TestCatalogReadsFirstUseNotesAndLinks(t *testing.T) {
	manifest := `
x-casaos:
  title: {en_US: Demo}
  developer: Demo Team
  website: "javascript:alert(1)"
  repo: https://github.com/demo/demo
  docs: /docs
  update_at: 2026-05-17
  tips:
    before_install:
      en_US: Default password is admin.
      zh_CN: 默认密码是 admin。
services: {}
`
	entries, err := appstore.LoadCatalog(fstest.MapFS{"memos/docker-compose.yml": {Data: []byte(manifest)}})
	if err != nil {
		t.Fatal(err)
	}
	app := entries[0].App
	want := appstore.App{
		ID: "memos", Title: "Demo", Category: appstore.CategoryNotes, Developer: "Demo Team",
		Repo: "https://github.com/demo/demo", Updated: "2026-05-17", Notes: "默认密码是 admin。", Scheme: "http", Path: "/",
	}
	if app != want {
		t.Fatalf("app = %+v, want %+v", app, want)
	}
}

// secretsFor generates the secrets an app's manifest needs.
func secretsFor(appID string) appstore.Secrets {
	secrets, _ := appstore.NewSecrets(appID, nil)
	return secrets
}
