package appstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

var testPolicy = appstore.Policy{
	AppDataRoot:   "/srv/a-nas/appdata",
	DataRoot:      "/srv/a-nas/data",
	PUID:          "1000",
	PGID:          "1000",
	TZ:            "Asia/Shanghai",
	ReservedPorts: []uint16{8080},
}

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
		plan, err := appstore.Render(context.Background(), entry, testPolicy)
		if err != nil {
			var policyError *appstore.PolicyError
			if errors.As(err, &policyError) {
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
