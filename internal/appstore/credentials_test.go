package appstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// Every fixed login or signing key in the catalog is replaced by a value
// generated for the install, and the owner is shown the logins.
func TestFixedCredentialsAreReplacedByGeneratedOnes(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	policy := Policy{AppDataRoot: "/srv/a-nas/data/apps", DataRoot: "/srv/a-nas/data/spaces/shared", TZ: "UTC", ReservedPorts: []uint16{8080}}
	identity := Identity{Username: "app-test", UID: 30010, GID: 30010}
	listed := map[string]bool{}
	for _, entry := range entries {
		listed[entry.App.ID] = true
		slots := appSecrets[entry.App.ID]
		if len(slots) == 0 {
			continue
		}
		if _, err := Render(context.Background(), entry, policy, identity, nil); !errors.Is(err, ErrSecretMissing) {
			t.Errorf("%s rendered without its secrets: %v", entry.App.ID, err)
		}
		secrets, changed := NewSecrets(entry.App.ID, nil)
		if !changed {
			t.Errorf("%s: no secrets generated", entry.App.ID)
		}
		plan, err := Render(context.Background(), entry, policy, identity, secrets)
		if err != nil {
			t.Fatalf("%s: %v", entry.App.ID, err)
		}
		compose := string(plan.Compose)
		for name, value := range secrets {
			if len(value) < 20 || !strings.Contains(compose, value) {
				t.Errorf("%s: secret %s (%q) is not in the compose file", entry.App.ID, name, value)
			}
		}
		for _, fixed := range []string{"PASSWORD: casaos", "PASS: casaos", "accessAuthCode=casaos", "ADMIN_PASSWORD: admin", "PSITRANSFER_ADMIN_PASS: secret", "aLongAndSecretString", "NEXTAUTH_SECRET: abc123"} {
			if strings.Contains(compose, fixed) {
				t.Errorf("%s still holds %q:\n%s", entry.App.ID, fixed, compose)
			}
		}
		if strings.Contains(strings.ToLower(entry.App.Notes), "casaos") {
			t.Errorf("%s notes still give the manifest's password:\n%s", entry.App.ID, entry.App.Notes)
		}
		for _, credential := range plan.Credentials {
			if credential.Label == "" || secrets[slotOf(entry.App.ID, credential.Label)] != credential.Secret {
				t.Errorf("%s: credential %+v does not match its secret", entry.App.ID, credential)
			}
		}
		// The same secrets render the same plan; they are kept, not redrawn.
		again, _ := NewSecrets(entry.App.ID, secrets)
		if second, err := Render(context.Background(), entry, policy, identity, again); err != nil || second.Digest != plan.Digest {
			t.Errorf("%s: plan changed with kept secrets: %v", entry.App.ID, err)
		}
	}
	for id := range appSecrets {
		if !listed[id] {
			t.Errorf("%s has secrets but is not in the catalog", id)
		}
	}
	openlist, _ := NewSecrets("openlist", nil)
	if got := Credentials("openlist", openlist); len(got) != 1 || got[0].Username != "admin" || got[0].Label != "管理员密码" {
		t.Errorf("OpenList credentials = %+v", got)
	}
	if got := Credentials("convertx", Secrets{"jwt": "x"}); len(got) != 0 {
		t.Errorf("an internal signing key is shown: %+v", got)
	}
}

// An older install still uses the manifest's own login, so the value kept
// for it must be the one the manifest sets.
func TestUpstreamCredentialsAreTheManifestsOwn(t *testing.T) {
	entries, err := Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, slot := range appSecrets[entry.App.ID] {
			if (slot.label == "") != (slot.upstream == "") {
				t.Errorf("%s %s: a login needs the manifest's value and only a login has one", entry.App.ID, slot.secret)
			}
			if slot.upstream == "" {
				continue
			}
			project, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
				WorkingDir:  projectDir,
				ConfigFiles: []types.ConfigFile{{Filename: "docker-compose.yml", Content: entry.Compose}},
				Environment: types.Mapping{"AppID": entry.App.ID},
			}, func(options *loader.Options) {
				options.SetProjectName(ProjectName(entry.App.ID), true)
				options.SkipResolveEnvironment = true
			})
			if err != nil {
				t.Fatalf("%s: %v", entry.App.ID, err)
			}
			service := project.Services[slot.service]
			var manifest string
			if slot.env != "" && service.Environment[slot.env] != nil {
				manifest = *service.Environment[slot.env]
			}
			for _, argument := range service.Command {
				if slot.arg != "" && strings.HasPrefix(argument, slot.arg) {
					manifest = strings.TrimPrefix(argument, slot.arg)
				}
			}
			if manifest != slot.upstream {
				t.Errorf("%s: manifest sets %q, the table keeps %q", entry.App.ID, manifest, slot.upstream)
			}
		}
	}
	got := UpstreamCredentials("transmission")
	if len(got) != 1 || !got[0].Upstream || got[0].Username != "casaos" {
		t.Errorf("Transmission upstream credentials = %+v", got)
	}
}

func slotOf(appID, label string) string {
	for _, slot := range appSecrets[appID] {
		if slot.label == label {
			return slot.secret
		}
	}
	return ""
}
