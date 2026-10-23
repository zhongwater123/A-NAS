package appstore

import (
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
)

// Some manifests write a login or a signing key into the Compose file as a
// fixed value: OpenList's admin password is casaos, ConvertX signs sessions
// with a key anyone can read upstream. Each install gets its own random
// values instead. They are generated once per app and kept by the engine, so
// a plan renders the same every time and a reinstall keeps them
// (Demo test report, item 2).
//
// Database passwords are left alone: those databases are reachable only on
// the app's own network, and they keep the password from their first start,
// so a new one would lock a reinstalled app out of the data it kept.

// Secrets are an app's generated values by name.
type Secrets map[string]string

// Credential is a login the App Center shows the owner.
type Credential struct {
	// Label says what it is, such as 管理员密码.
	Label string
	// Username is the account it belongs to; empty when there is none.
	Username string
	Secret   string
	// Upstream marks the manifest's own value, which anyone can look up: the
	// app was installed before A-NAS generated its logins.
	Upstream bool
}

var ErrSecretMissing = errors.New("an app secret is missing")

// secretSlot is one place a manifest holds a fixed credential.
type secretSlot struct {
	service string
	// env names the environment variable; arg is a command-line prefix
	// such as --accessAuthCode= instead.
	env, arg string
	// secret names the generated value.
	secret string
	// label and username show the value to the owner; internal keys have
	// no label.
	label, username string
	// upstream is the value the manifest sets, which installs from before
	// generated logins still use. OpenList and PhotoPrism set their admin
	// password only on first start, so even a reinstall that keeps the data
	// keeps it.
	upstream string
}

var appSecrets = map[string][]secretSlot{
	"convertx":   {{service: "convertx", env: "JWT_SECRET", secret: "jwt"}},
	"etherpad":   {{service: "etherpad", env: "ADMIN_PASSWORD", secret: "admin", label: "管理后台密码", username: "admin", upstream: "admin"}},
	"linkwarden": {{service: "linkwarden", env: "NEXTAUTH_SECRET", secret: "nextauth"}},
	"openlist":   {{service: "openlist", env: "OPENLIST_ADMIN_PASSWORD", secret: "admin", label: "管理员密码", username: "admin", upstream: "casaos"}},
	"photoprism": {{service: "photoprism", env: "PHOTOPRISM_ADMIN_PASSWORD", secret: "admin", label: "管理员密码", username: "admin", upstream: "casaos"}},
	"psitransfer": {
		{service: "psitransfer", env: "PSITRANSFER_ADMIN_PASS", secret: "admin", label: "管理页 /admin 密码", upstream: "secret"},
	},
	"siyuan-note": {{service: "siyuan-note", arg: "--accessAuthCode=", secret: "access", label: "访问授权码", upstream: "casaos"}},
	// The account keeps the manifest's name, so older installs keep theirs.
	"transmission": {{service: "transmission", env: "PASS", secret: "admin", label: "密码", username: "casaos", upstream: "casaos"}},
}

// NewSecrets returns secrets with a value for every name the app needs,
// keeping the values it already has; changed says whether any was added.
func NewSecrets(appID string, existing Secrets) (Secrets, bool) {
	secrets := Secrets{}
	maps.Copy(secrets, existing)
	changed := false
	for _, slot := range appSecrets[appID] {
		if secrets[slot.secret] == "" {
			secrets[slot.secret] = rand.Text()
			changed = true
		}
	}
	return secrets, changed
}

// UpstreamCredentials lists the manifest's own logins of an app, for an
// install from before A-NAS generated them.
func UpstreamCredentials(appID string) []Credential {
	var credentials []Credential
	for _, slot := range appSecrets[appID] {
		if slot.label != "" && slot.upstream != "" {
			credentials = append(credentials, Credential{Label: slot.label, Username: slot.username, Secret: slot.upstream, Upstream: true})
		}
	}
	return credentials
}

// Credentials lists the logins of an app with their values.
func Credentials(appID string, secrets Secrets) []Credential {
	var credentials []Credential
	for _, slot := range appSecrets[appID] {
		if slot.label != "" && secrets[slot.secret] != "" {
			credentials = append(credentials, Credential{Label: slot.label, Username: slot.username, Secret: secrets[slot.secret]})
		}
	}
	return credentials
}

// applySecrets writes an app's generated values into a service.
func applySecrets(appID, name string, service *types.ServiceConfig, secrets Secrets) error {
	for _, slot := range appSecrets[appID] {
		if slot.service != name {
			continue
		}
		value := secrets[slot.secret]
		if value == "" {
			return fmt.Errorf("%w: %s %s", ErrSecretMissing, appID, slot.secret)
		}
		switch {
		case slot.env != "":
			if service.Environment == nil {
				service.Environment = types.MappingWithEquals{}
			}
			service.Environment[slot.env] = &value
		case slot.arg != "":
			replaced := false
			for i, argument := range service.Command {
				if strings.HasPrefix(argument, slot.arg) {
					service.Command[i] = slot.arg + value
					replaced = true
				}
			}
			if !replaced {
				return fmt.Errorf("%w: %s has no %s argument", ErrSecretMissing, name, slot.arg)
			}
		}
	}
	return nil
}
