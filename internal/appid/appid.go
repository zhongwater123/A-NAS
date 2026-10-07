// Package appid holds the identity facts shared by the App Center and the
// Host Agent: which app IDs are valid and which Linux account each app runs
// as (ADR 0008). It has no dependencies so the root Host Agent stays small.
package appid

import (
	"context"
	"regexp"
)

var pattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

// Valid reports whether id can name a catalog app and its Compose project.
func Valid(id string) bool {
	return pattern.MatchString(id)
}

// App identities use their own UID/GID range, outside member accounts
// (20100-29999) and system accounts. A UID is never reused.
const (
	FirstUID = 30000
	LastUID  = 30999
)

// Identity is the Linux account app-<id> an app's containers run as.
type Identity struct {
	Username string
	UID      int
	GID      int
}

// Username is the Linux account name of an app.
func Username(id string) string {
	return "app-" + id
}

// Host prepares the Linux side of an app: its identity, the folders it
// mounts, and its access to the Shared folder. The Host Agent implements it.
type Host interface {
	AppIdentity(ctx context.Context, id string) (Identity, error)
	PrepareApp(ctx context.Context, id string, folders []string, shared bool) error
	ReleaseApp(ctx context.Context, id string) error
}
