package main

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"time"

	containeragent "github.com/zhongwater123/A-NAS/internal/containers/agent"
	"github.com/zhongwater123/A-NAS/internal/envconfig"
	hostagent "github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/mediaworker"
)

// Mode is how the Product Service reaches the host (ANAS_HOSTSTATE_MODE).
// It has no default: a NAS whose environment file lost the line must not
// start on simulated storage (#192).
type Mode string

const (
	// ModeAgent is a NAS: host state, privileged operations and every file
	// go through the Host Agent and its File Broker, photos and shares
	// through their own services, and FFmpeg through the media worker.
	ModeAgent Mode = "agent"
	// ModeFake is development and tests: simulated disks and storage, a
	// local development volume, and the photo library, FFmpeg and Fake
	// containers in this process.
	ModeFake Mode = "fake"
)

// Where FFmpeg runs (ANAS_MEDIA_WORKER).
const (
	mediaWorkerSocket   = "socket"
	mediaWorkerLocal    = "local"
	mediaWorkerDisabled = "disabled"
)

// How containers and apps are managed (ANAS_CONTAINERS_MODE).
const (
	containersFake     = "fake"
	containersAgent    = "agent"
	containersDisabled = "disabled"
)

// Config is the Product Service's configuration, resolved from the
// environment once at startup. loadConfig refuses an agent configuration
// that still carries a development adapter.
type Config struct {
	Mode Mode `env:"ANAS_HOSTSTATE_MODE"`
	// HTTPAddr must be a loopback address: the LAN reaches the API only
	// through Caddy (invariant 9), and the loopback checks for setup, the
	// local console and the terminal rely on it.
	HTTPAddr string `env:"ANAS_HTTP_ADDR" envDefault:"127.0.0.1:8080"`
	// StateDir holds the SQLite stores and caches; it defaults to the user's
	// configuration directory.
	StateDir string `env:"ANAS_STATE_DIR"`
	// DataMount is the data volume; development defaults to a directory in
	// StateDir.
	DataMount string `env:"ANAS_DATA_MOUNT"`
	// HostAgentSocket defaults to the runtime directory of a user service.
	HostAgentSocket string `env:"ANAS_HOST_AGENT_SOCKET"`
	RuntimeDir      string `env:"XDG_RUNTIME_DIR"`
	// FileBrokerSocket defaults to the socket beside the Host Agent's.
	FileBrokerSocket string `env:"ANAS_FILE_BROKER_SOCKET"`
	PhotosSocket     string `env:"ANAS_PHOTOS_SOCKET" envDefault:"/run/a-nas-photos/photos.sock"`
	SharesSocket     string `env:"ANAS_SHARES_SOCKET" envDefault:"/run/a-nas-shares/shares.sock"`
	ShareFilesSocket string `env:"ANAS_SHARE_FILES_SOCKET" envDefault:"/run/a-nas-share-files/files.sock"`
	// MediaWorker is socket, local (FFmpeg in this process, development
	// only) or disabled; it defaults to socket on a NAS and local otherwise.
	MediaWorker string `env:"ANAS_MEDIA_WORKER"`
	Media       MediaConfig
	// DevelopmentAISocket names an AI Worker for the photo library that
	// development runs in this process; on a NAS the photo service has its
	// own.
	DevelopmentAISocket string `env:"ANAS_AI_SOCKET"`
	// Containers is fake, agent or disabled; it defaults to fake in
	// development and disabled on a NAS, so a NAS never shows invented
	// containers or apps.
	Containers           string `env:"ANAS_CONTAINERS_MODE"`
	ContainerAgentSocket string `env:"ANAS_CONTAINER_AGENT_SOCKET"`
	// Terminal is enabled or disabled, the default (docs/specs/web-terminal.md).
	Terminal        string `env:"ANAS_TERMINAL" envDefault:"disabled"`
	TerminalEnabled bool   `env:"-"`
	// ScreensaverDirectory holds the local console's screen saver videos.
	ScreensaverDirectory string `env:"ANAS_SCREENSAVER_DIRECTORY"`
	// ScreensaverVideo is the variable releases before the pool named; its
	// directory is the pool.
	ScreensaverVideo string `env:"ANAS_SCREENSAVER_VIDEO"`
	Assistant        AssistantConfig
	// HostAgentChangesSocket is the Host Agent's socket for the assistants'
	// changes. The Product Service once read the same name for the spike's
	// change broker; it refuses it now, so that neither meaning is taken for
	// the other.
	HostAgentChangesSocket string `env:"ANAS_ASSISTANT_CHANGES_SOCKET"`
}

// MediaConfig reaches the sandboxed media worker. The Product, Photo and
// Share Services take their turns from the same slot directory, which the
// installer's tmpfiles entry creates, so together they never ask for more
// workers than the socket admits.
type MediaConfig struct {
	Socket string `env:"ANAS_MEDIA_SOCKET" envDefault:"/run/a-nas-media/media.sock"`
	Slots  string `env:"ANAS_MEDIA_SLOTS" envDefault:"/run/a-nas-media/slots"`
}

func (m MediaConfig) client() mediaworker.Client {
	return mediaworker.Client{Socket: m.Socket, Slots: m.Slots}
}

// AssistantConfig configures the AI assistant (ADR 0024), which is off
// unless SocketDir names the members' instance sockets.
type AssistantConfig struct {
	SocketDir string `env:"ANAS_ASSISTANT_SOCKET_DIR"`
	// TokenDir lets development dial the instance sockets directly with
	// their tokens; a NAS always connects through the File Broker's session
	// gate (decision 10).
	TokenDir string `env:"ANAS_ASSISTANT_TOKEN_DIR"`
	// SpikeBrokerSocket is the spike's change broker
	// (ai/assistant-spike/change_broker.py), which executes the assistant's
	// changes in development; a NAS uses the File Broker.
	SpikeBrokerSocket string `env:"ANAS_ASSISTANT_SPIKE_BROKER_SOCKET"`
}

// Live says the Product Service runs on a NAS through the Host Agent.
func (c Config) Live() bool { return c.Mode == ModeAgent }

// loadConfig resolves and validates the Product Service's configuration. It
// reports every problem at once, each naming its variable.
func loadConfig(environment map[string]string) (Config, error) {
	c, err := envconfig.Parse[Config](environment)
	if err != nil {
		return Config{}, err
	}
	var problems []error
	problem := func(err error) {
		if err != nil {
			problems = append(problems, err)
		}
	}
	path := func(name string, value *string) {
		cleaned, err := envconfig.AbsolutePath(name, *value)
		problem(err)
		*value = cleaned
	}
	switch c.Mode {
	case ModeAgent, ModeFake:
	case "":
		return Config{}, errors.New("ANAS_HOSTSTATE_MODE is not set: agent on a NAS, fake for development")
	default:
		return Config{}, errors.New("ANAS_HOSTSTATE_MODE must be agent or fake")
	}
	live := c.Live()
	problem(loopbackAddress(c.HTTPAddr))

	if c.StateDir != "" {
		path("ANAS_STATE_DIR", &c.StateDir)
	} else if directory, err := os.UserConfigDir(); err != nil {
		problem(fmt.Errorf("ANAS_STATE_DIR is not set and there is no user configuration directory: %w", err))
	} else {
		c.StateDir = filepath.Join(directory, "a-nas", "state")
	}
	path("ANAS_DATA_MOUNT", &c.DataMount)
	if c.DataMount == "" {
		c.DataMount = filepath.Join(c.StateDir, "development-volume")
		if live {
			c.DataMount = "/srv/a-nas/data"
		}
	}
	// Problems are reported in the order of these lists, the same every time.
	for _, variable := range []struct {
		name  string
		value *string
	}{
		{"ANAS_PHOTOS_SOCKET", &c.PhotosSocket}, {"ANAS_SHARES_SOCKET", &c.SharesSocket},
		{"ANAS_SHARE_FILES_SOCKET", &c.ShareFilesSocket}, {"ANAS_MEDIA_SOCKET", &c.Media.Socket},
		{"ANAS_MEDIA_SLOTS", &c.Media.Slots}, {"ANAS_SCREENSAVER_DIRECTORY", &c.ScreensaverDirectory},
		{"ANAS_ASSISTANT_SOCKET_DIR", &c.Assistant.SocketDir},
	} {
		path(variable.name, variable.value)
	}
	if live {
		socket, err := hostagent.SocketPath(c.HostAgentSocket, c.RuntimeDir)
		problem(err)
		c.HostAgentSocket = socket
		path("ANAS_FILE_BROKER_SOCKET", &c.FileBrokerSocket)
		if c.FileBrokerSocket == "" && socket != "" {
			c.FileBrokerSocket = filepath.Join(filepath.Dir(socket), "file-broker.sock")
		}
	}

	if c.MediaWorker == "" {
		c.MediaWorker = mediaWorkerLocal
		if live {
			c.MediaWorker = mediaWorkerSocket
		}
	}
	switch c.MediaWorker {
	case mediaWorkerSocket, mediaWorkerDisabled:
	case mediaWorkerLocal:
		if live {
			problem(errors.New("ANAS_MEDIA_WORKER=local runs FFmpeg in the Product Service, which a NAS never does (invariant 16)"))
		}
	default:
		problem(errors.New("ANAS_MEDIA_WORKER must be socket, local or disabled"))
	}

	if c.Containers == "" {
		c.Containers = containersFake
		if live {
			c.Containers = containersDisabled
		}
	}
	switch c.Containers {
	case containersDisabled:
	case containersAgent:
		socket, err := containeragent.SocketPath(c.ContainerAgentSocket)
		problem(err)
		c.ContainerAgentSocket = socket
	case containersFake:
		if live {
			problem(errors.New("ANAS_CONTAINERS_MODE=fake shows invented containers and apps; a NAS uses agent or disabled"))
		}
	default:
		problem(errors.New("ANAS_CONTAINERS_MODE must be fake, agent or disabled"))
	}

	switch c.Terminal {
	case "enabled":
		c.TerminalEnabled = true
	case "disabled":
	default:
		problem(errors.New("ANAS_TERMINAL must be enabled or disabled"))
	}

	if c.ScreensaverDirectory == "" {
		c.ScreensaverDirectory = "/var/lib/a-nas/screensavers/current"
		if c.ScreensaverVideo != "" {
			c.ScreensaverDirectory = filepath.Dir(c.ScreensaverVideo)
		}
	}

	if c.HostAgentChangesSocket != "" {
		problem(errors.New("ANAS_ASSISTANT_CHANGES_SOCKET is the Host Agent's; the Product Service's stand-in for development is ANAS_ASSISTANT_SPIKE_BROKER_SOCKET"))
	}
	if live {
		for _, variable := range []struct{ name, value string }{
			{"ANAS_AI_SOCKET", c.DevelopmentAISocket},
			{"ANAS_ASSISTANT_TOKEN_DIR", c.Assistant.TokenDir},
			{"ANAS_ASSISTANT_SPIKE_BROKER_SOCKET", c.Assistant.SpikeBrokerSocket},
		} {
			if variable.value != "" {
				problem(fmt.Errorf("%s is for development only; on a NAS that goes through its own service", variable.name))
			}
		}
	}
	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return c, nil
}

// loopbackAddress accepts only a loopback IP and a port.
func loopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err == nil {
		var ip netip.Addr
		if ip, err = netip.ParseAddr(host); err == nil && !ip.IsLoopback() {
			err = errors.New("not loopback")
		}
	}
	if err != nil {
		return fmt.Errorf("ANAS_HTTP_ADDR must be a loopback address and port such as 127.0.0.1:8080; the LAN reaches the API only through Caddy: %w", err)
	}
	return nil
}

// photoServiceConfig configures the photo service (ADR 0011), which runs
// from this binary as its own process and identity.
type photoServiceConfig struct {
	Root          string `env:"ANAS_PHOTOS_ROOT" envDefault:"/srv/a-nas/data/photos"`
	Socket        string `env:"ANAS_PHOTOS_SOCKET" envDefault:"/run/a-nas-photos/photos.sock"`
	SessionSocket string `env:"ANAS_PHOTO_SESSION_SOCKET" envDefault:"/run/a-nas-sessions/photos.sock"`
	// AssistantSocket serves members' AI assistants (ADR 0024), which are
	// not shipped yet, so it is off unless named. A socket unit may hold it
	// (FileDescriptorName=assistant) so that it outlives restarts.
	AssistantSocket string `env:"ANAS_PHOTOS_ASSISTANT_SOCKET"`
	// Without the AI component the socket is absent and photos wait for it.
	AISocket string `env:"ANAS_AI_SOCKET" envDefault:"/run/a-nas-ai/ai.sock"`
	// VectorSocket reaches the photo service's own vector index (ADR 0028);
	// the unit names it, and without it the index is off.
	VectorSocket string `env:"ANAS_PHOTOS_VECTOR_SOCKET"`
	// QuietPeriod shortens how long the library must go unused before AI
	// work starts, for the system test; products keep the default.
	QuietPeriod string        `env:"ANAS_PHOTOS_AI_QUIET_PERIOD"`
	Quiet       time.Duration `env:"-"`
	Media       MediaConfig
}

// loadPhotoServiceConfig resolves and validates the photo service's
// configuration, reporting every problem at once.
func loadPhotoServiceConfig(environment map[string]string) (photoServiceConfig, error) {
	c, err := envconfig.Parse[photoServiceConfig](environment)
	if err != nil {
		return photoServiceConfig{}, err
	}
	problems := absolutePaths(map[string]*string{
		"ANAS_PHOTOS_ROOT": &c.Root, "ANAS_PHOTOS_SOCKET": &c.Socket, "ANAS_PHOTO_SESSION_SOCKET": &c.SessionSocket,
		"ANAS_PHOTOS_ASSISTANT_SOCKET": &c.AssistantSocket, "ANAS_AI_SOCKET": &c.AISocket, "ANAS_PHOTOS_VECTOR_SOCKET": &c.VectorSocket,
		"ANAS_MEDIA_SOCKET": &c.Media.Socket, "ANAS_MEDIA_SLOTS": &c.Media.Slots,
	})
	if c.QuietPeriod != "" {
		if c.Quiet, err = time.ParseDuration(c.QuietPeriod); err != nil || c.Quiet <= 0 {
			problems = append(problems, errors.New("ANAS_PHOTOS_AI_QUIET_PERIOD must be a positive duration such as 5s"))
		}
	}
	if len(problems) > 0 {
		return photoServiceConfig{}, errors.Join(problems...)
	}
	return c, nil
}

// absolutePaths cleans each variable's path and names, in the order of the
// names, every one that is not absolute.
func absolutePaths(variables map[string]*string) []error {
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(variables)) {
		cleaned, err := envconfig.AbsolutePath(name, *variables[name])
		if err != nil {
			problems = append(problems, err)
		}
		*variables[name] = cleaned
	}
	return problems
}

// shareServiceConfig configures the share service (ADR 0023), which runs
// from this binary as its own process and identity.
type shareServiceConfig struct {
	Root          string `env:"ANAS_SHARES_ROOT" envDefault:"/srv/a-nas/data/shares"`
	Socket        string `env:"ANAS_SHARES_SOCKET" envDefault:"/run/a-nas-shares/shares.sock"`
	FilesSocket   string `env:"ANAS_SHARE_FILES_SOCKET" envDefault:"/run/a-nas-share-files/files.sock"`
	SessionSocket string `env:"ANAS_SHARE_SESSION_SOCKET" envDefault:"/run/a-nas-share-sessions/shares.sock"`
	Media         MediaConfig
}

// loadShareServiceConfig resolves and validates the share service's
// configuration, reporting every problem at once.
func loadShareServiceConfig(environment map[string]string) (shareServiceConfig, error) {
	c, err := envconfig.Parse[shareServiceConfig](environment)
	if err != nil {
		return shareServiceConfig{}, err
	}
	problems := absolutePaths(map[string]*string{
		"ANAS_SHARES_ROOT": &c.Root, "ANAS_SHARES_SOCKET": &c.Socket, "ANAS_SHARE_FILES_SOCKET": &c.FilesSocket,
		"ANAS_SHARE_SESSION_SOCKET": &c.SessionSocket, "ANAS_MEDIA_SOCKET": &c.Media.Socket, "ANAS_MEDIA_SLOTS": &c.Media.Slots,
	})
	if len(problems) > 0 {
		return shareServiceConfig{}, errors.Join(problems...)
	}
	return c, nil
}
