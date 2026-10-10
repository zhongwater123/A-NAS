// Package appstore turns curated CasaOS-AppStore Compose manifests into
// validated install plans. Every manifest is checked against Policy before it
// can run: no privileged or host-namespace options, no devices or Docker
// socket, published ports at or above 1024, and bind mounts only below the
// app's own data folder and the Shared folder. Each app runs as its own Linux
// identity app-<id> (ADR 0008). Installing requires the digest of the plan
// the user confirmed, so the executed Compose file is exactly the one shown.
package appstore

import (
	"context"
	"errors"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appid"
)

var (
	ErrNotFound         = errors.New("app not found")
	ErrPlanChanged      = errors.New("install plan changed since it was confirmed")
	ErrBusy             = errors.New("another app operation is running")
	ErrAlreadyInstalled = errors.New("app is already installed")
	ErrNotInstalled     = errors.New("app is not installed")
	ErrPortInUse        = errors.New("a published port is already in use")
	ErrNameInUse        = errors.New("a container name is already in use")
	ErrUnavailable      = errors.New("app store engine is unavailable")
	// ErrAddressPoolMissing refuses apps that create Docker networks while
	// Docker allocates from its built-in pools, which overlap common LAN
	// ranges (ADR 0013).
	ErrAddressPoolMissing = errors.New("Docker has no address pool for app networks")
	// ErrNetworkOutsidePool fails an install whose network Docker placed
	// outside the address pools; the install is rolled back.
	ErrNetworkOutsidePool = errors.New("an app network is outside the Docker address pools")
	// ErrRuntimeOutdated refuses installs on a Docker that drops volume
	// subpaths, Compose before 2.30 or an engine API before 1.45: the app
	// would get its whole app-data or Shared volume (ADR 0015).
	ErrRuntimeOutdated = errors.New("Docker Compose or Engine is too old for A-NAS app mounts")
	// ErrMountOutsideFolder fails an install whose container mounts an A-NAS
	// volume beyond the folder in its plan; the install is rolled back.
	ErrMountOutsideFolder = errors.New("an app mount is not limited to its planned folder")
)

// ValidID reports whether id can name a catalog app and its Compose project.
func ValidID(id string) bool {
	return appid.Valid(id)
}

// ProjectName is the Compose project of an installed app; the prefix keeps
// A-NAS apps apart from projects the owner created by hand.
func ProjectName(id string) string {
	return "a-nas-" + id
}

// AppLabel marks every container an app install creates.
const AppLabel = "io.a-nas.app"

type App struct {
	ID          string
	Title       string
	Tagline     string
	Description string
	Category    string
	Version     string
	Author      string
	Website     string
	// WebPort is the published port of the app's web interface, 0 if none.
	WebPort uint16
	Scheme  string
	Path    string
}

type InstallState string

const (
	StateAvailable    InstallState = "available"
	StateInstalled    InstallState = "installed"
	StateInstalling   InstallState = "installing"
	StateUninstalling InstallState = "uninstalling"
)

type JobAction string

const (
	JobInstall   JobAction = "install"
	JobUninstall JobAction = "uninstall"
)

type JobState string

const (
	JobRunning   JobState = "running"
	JobSucceeded JobState = "succeeded"
	JobFailed    JobState = "failed"
)

// Job is the latest install or uninstall run of one app.
type Job struct {
	AppID      string
	Action     JobAction
	State      JobState
	StartedAt  time.Time
	FinishedAt time.Time
	// Output is the tail of the Compose command output.
	Output []string
	Error  string
}

type AppStatus struct {
	App
	State InstallState
	// Running counts running containers of an installed app.
	Running int
	Total   int
	Job     *Job
}

// Identity is the Linux account app-<id> an app's containers run as; the
// Host Agent allocates it (ADR 0008).
type Identity = appid.Identity

const (
	FirstAppUID = appid.FirstUID
	LastAppUID  = appid.LastUID
)

// IdentityName is the Linux account name of an app.
func IdentityName(id string) string {
	return appid.Username(id)
}

type Store interface {
	Apps(ctx context.Context) ([]AppStatus, error)
	Icon(ctx context.Context, id string) ([]byte, string, error)
	Plan(ctx context.Context, id string, identity Identity) (Plan, error)
	Install(ctx context.Context, id, digest string, identity Identity) (Job, error)
	Uninstall(ctx context.Context, id string) (Job, error)
}
