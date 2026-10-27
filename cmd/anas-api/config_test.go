package main

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestConfigResolvesEachModesAdaptersAndRefusesDevelopmentOnesOnANAS(t *testing.T) {
	// agent is the smallest NAS environment; tests add to a copy.
	agent := map[string]string{"ANAS_HOSTSTATE_MODE": "agent", "ANAS_HOST_AGENT_SOCKET": "/run/a-nas/host-agent.sock", "ANAS_STATE_DIR": "/var/lib/a-nas"}
	fake := map[string]string{"ANAS_HOSTSTATE_MODE": "fake", "ANAS_STATE_DIR": "/home/dev/state"}
	with := func(base map[string]string, extra ...string) map[string]string {
		environment := maps.Clone(base)
		for i := 0; i < len(extra); i += 2 {
			environment[extra[i]] = extra[i+1]
		}
		return environment
	}
	tests := []struct {
		name        string
		environment map[string]string
		// wantError lists what the error must say; check inspects a loaded config.
		wantError []string
		check     func(*testing.T, Config)
	}{
		{name: "no mode", environment: map[string]string{"ANAS_STATE_DIR": "/var/lib/a-nas"}, wantError: []string{"ANAS_HOSTSTATE_MODE is not set"}},
		{name: "blank mode", environment: with(agent, "ANAS_HOSTSTATE_MODE", "  "), wantError: []string{"ANAS_HOSTSTATE_MODE is not set"}},
		{name: "unknown mode", environment: with(agent, "ANAS_HOSTSTATE_MODE", "live"), wantError: []string{"ANAS_HOSTSTATE_MODE must be agent or fake"}},
		{name: "NAS defaults", environment: agent, check: func(t *testing.T, c Config) {
			want := Config{
				Mode: ModeAgent, HTTPAddr: "127.0.0.1:8080", StateDir: "/var/lib/a-nas", DataMount: "/srv/a-nas/data",
				HostAgentSocket: "/run/a-nas/host-agent.sock", FileBrokerSocket: "/run/a-nas/file-broker.sock",
				PhotosSocket: "/run/a-nas-photos/photos.sock", SharesSocket: "/run/a-nas-shares/shares.sock",
				ShareFilesSocket: "/run/a-nas-share-files/files.sock", MediaWorker: mediaWorkerSocket,
				Media:      MediaConfig{Socket: "/run/a-nas-media/media.sock", Slots: "/run/a-nas-media/slots"},
				Containers: containersDisabled, Terminal: "disabled", ScreensaverDirectory: "/var/lib/a-nas/screensavers/current",
			}
			if c != want {
				t.Fatalf("config = %+v\nwant     %+v", c, want)
			}
			if !c.Live() {
				t.Fatal("an agent configuration is not live")
			}
		}},
		{name: "user service on a NAS", environment: map[string]string{"ANAS_HOSTSTATE_MODE": "agent", "XDG_RUNTIME_DIR": "/run/user/1000", "ANAS_STATE_DIR": "/home/anas-dev/.config/a-nas/state"}, check: func(t *testing.T, c Config) {
			if c.HostAgentSocket != "/run/user/1000/a-nas/host-agent.sock" || c.FileBrokerSocket != "/run/user/1000/a-nas/file-broker.sock" {
				t.Fatalf("sockets = %q, %q", c.HostAgentSocket, c.FileBrokerSocket)
			}
		}},
		{name: "NAS without a Host Agent socket", environment: map[string]string{"ANAS_HOSTSTATE_MODE": "agent", "ANAS_STATE_DIR": "/var/lib/a-nas"}, wantError: []string{"XDG_RUNTIME_DIR"}},
		{name: "development defaults", environment: fake, check: func(t *testing.T, c Config) {
			if c.Live() || c.DataMount != "/home/dev/state/development-volume" || c.MediaWorker != mediaWorkerLocal || c.Containers != containersFake {
				t.Fatalf("config = %+v", c)
			}
			if c.HostAgentSocket != "" || c.FileBrokerSocket != "" {
				t.Fatalf("development reaches a Host Agent: %+v", c)
			}
		}},
		{name: "development state directory", environment: map[string]string{"ANAS_HOSTSTATE_MODE": "fake"}, check: func(t *testing.T, c Config) {
			directory, err := os.UserConfigDir()
			if err != nil {
				t.Skip("no user configuration directory:", err)
			}
			if want := filepath.Join(directory, "a-nas", "state"); c.StateDir != want {
				t.Fatalf("state directory = %q, want %q", c.StateDir, want)
			}
		}},
		{name: "trimmed values", environment: with(fake, "ANAS_HTTP_ADDR", " 127.0.0.1:18080 ", "ANAS_TERMINAL", "enabled\n", "ANAS_DATA_MOUNT", " "), check: func(t *testing.T, c Config) {
			if c.HTTPAddr != "127.0.0.1:18080" || !c.TerminalEnabled || c.DataMount != "/home/dev/state/development-volume" {
				t.Fatalf("config = %+v", c)
			}
		}},
		{name: "IPv6 loopback", environment: with(agent, "ANAS_HTTP_ADDR", "[::1]:8080"), check: func(t *testing.T, c Config) {
			if c.HTTPAddr != "[::1]:8080" {
				t.Fatalf("address = %q", c.HTTPAddr)
			}
		}},
		{name: "every interface", environment: with(fake, "ANAS_HTTP_ADDR", ":8080"), wantError: []string{"ANAS_HTTP_ADDR must be a loopback address"}},
		{name: "unspecified address", environment: with(agent, "ANAS_HTTP_ADDR", "0.0.0.0:8080"), wantError: []string{"ANAS_HTTP_ADDR must be a loopback address"}},
		{name: "LAN address", environment: with(agent, "ANAS_HTTP_ADDR", "192.168.1.20:8080"), wantError: []string{"ANAS_HTTP_ADDR must be a loopback address"}},
		{name: "host name", environment: with(fake, "ANAS_HTTP_ADDR", "localhost:8080"), wantError: []string{"ANAS_HTTP_ADDR must be a loopback address"}},
		{name: "no port", environment: with(fake, "ANAS_HTTP_ADDR", "127.0.0.1"), wantError: []string{"ANAS_HTTP_ADDR must be a loopback address"}},
		{name: "relative state directory", environment: with(fake, "ANAS_STATE_DIR", "state"), wantError: []string{"ANAS_STATE_DIR must be an absolute path"}},
		{name: "relative sockets", environment: with(agent, "ANAS_FILE_BROKER_SOCKET", "file-broker.sock", "ANAS_PHOTOS_SOCKET", "photos.sock"), wantError: []string{
			"ANAS_FILE_BROKER_SOCKET must be an absolute path", "ANAS_PHOTOS_SOCKET must be an absolute path",
		}},
		{name: "FFmpeg in the Product Service on a NAS", environment: with(agent, "ANAS_MEDIA_WORKER", "local"), wantError: []string{"ANAS_MEDIA_WORKER=local"}},
		{name: "FFmpeg in development", environment: with(fake, "ANAS_MEDIA_WORKER", "socket"), check: func(t *testing.T, c Config) {
			if c.MediaWorker != mediaWorkerSocket {
				t.Fatalf("media worker = %q", c.MediaWorker)
			}
		}},
		{name: "media worker off on a NAS", environment: with(agent, "ANAS_MEDIA_WORKER", "disabled"), check: func(t *testing.T, c Config) {
			if c.MediaWorker != mediaWorkerDisabled {
				t.Fatalf("media worker = %q", c.MediaWorker)
			}
		}},
		{name: "unknown media worker", environment: with(fake, "ANAS_MEDIA_WORKER", "remote"), wantError: []string{"ANAS_MEDIA_WORKER must be socket, local or disabled"}},
		{name: "Fake containers on a NAS", environment: with(agent, "ANAS_CONTAINERS_MODE", "fake"), wantError: []string{"ANAS_CONTAINERS_MODE=fake"}},
		{name: "container agent", environment: with(agent, "ANAS_CONTAINERS_MODE", "agent"), check: func(t *testing.T, c Config) {
			if c.Containers != containersAgent || c.ContainerAgentSocket != "/run/a-nas-container/agent.sock" {
				t.Fatalf("containers = %q at %q", c.Containers, c.ContainerAgentSocket)
			}
		}},
		{name: "relative container agent socket", environment: with(agent, "ANAS_CONTAINERS_MODE", "agent", "ANAS_CONTAINER_AGENT_SOCKET", "agent.sock"), wantError: []string{"ANAS_CONTAINER_AGENT_SOCKET must be an absolute path"}},
		{name: "unknown containers mode", environment: with(fake, "ANAS_CONTAINERS_MODE", "docker"), wantError: []string{"ANAS_CONTAINERS_MODE must be fake, agent or disabled"}},
		{name: "unknown terminal", environment: with(agent, "ANAS_TERMINAL", "on"), wantError: []string{"ANAS_TERMINAL must be enabled or disabled"}},
		{name: "screen saver video of earlier releases", environment: with(agent, "ANAS_SCREENSAVER_VIDEO", "/var/lib/a-nas/screensaver/loop.mp4"), check: func(t *testing.T, c Config) {
			if c.ScreensaverDirectory != "/var/lib/a-nas/screensaver" {
				t.Fatalf("screen saver directory = %q", c.ScreensaverDirectory)
			}
		}},
		{name: "development AI on a NAS", environment: with(agent, "ANAS_AI_SOCKET", "/run/a-nas-ai/ai.sock"), wantError: []string{"ANAS_AI_SOCKET is for development only"}},
		{name: "assistant dialled directly on a NAS", environment: with(agent, "ANAS_ASSISTANT_SOCKET_DIR", "/run/a-nas-assistant", "ANAS_ASSISTANT_TOKEN_DIR", "/etc/a-nas-assistant/tokens"), wantError: []string{"ANAS_ASSISTANT_TOKEN_DIR is for development only"}},
		{name: "spike change broker on a NAS", environment: with(agent, "ANAS_ASSISTANT_SPIKE_BROKER_SOCKET", "/run/anas-spike-broker/broker.sock"), wantError: []string{"ANAS_ASSISTANT_SPIKE_BROKER_SOCKET is for development only"}},
		{name: "assistant in development", environment: with(fake, "ANAS_ASSISTANT_SOCKET_DIR", "/run/a-nas-assistant", "ANAS_ASSISTANT_TOKEN_DIR", "/tmp/tokens", "ANAS_ASSISTANT_SPIKE_BROKER_SOCKET", "/run/anas-spike-broker/broker.sock"), check: func(t *testing.T, c Config) {
			want := AssistantConfig{SocketDir: "/run/a-nas-assistant", TokenDir: "/tmp/tokens", SpikeBrokerSocket: "/run/anas-spike-broker/broker.sock"}
			if c.Assistant != want {
				t.Fatalf("assistant = %+v", c.Assistant)
			}
		}},
		{name: "the Host Agent's changes socket", environment: with(fake, "ANAS_ASSISTANT_CHANGES_SOCKET", "/run/a-nas/assistant-changes.sock"), wantError: []string{"ANAS_ASSISTANT_CHANGES_SOCKET is the Host Agent's"}},
		{name: "every problem at once", environment: with(agent, "ANAS_HTTP_ADDR", "0.0.0.0:8080", "ANAS_MEDIA_WORKER", "local", "ANAS_CONTAINERS_MODE", "fake"), wantError: []string{
			"ANAS_HTTP_ADDR", "ANAS_MEDIA_WORKER=local", "ANAS_CONTAINERS_MODE=fake",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadConfig(test.environment)
			if len(test.wantError) > 0 {
				if err == nil {
					t.Fatalf("loadConfig() = %+v, want an error", config)
				}
				for _, want := range test.wantError {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not say %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			test.check(t, config)
		})
	}
}

// A relative state directory is reported as such, also where the default
// could not be found either, and every problem comes in the same order.
func TestConfigNamesEveryProblemInTheSameOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	environment := map[string]string{
		"ANAS_HOSTSTATE_MODE": "agent", "ANAS_HOST_AGENT_SOCKET": "/run/a-nas/host-agent.sock", "ANAS_STATE_DIR": "state",
		"ANAS_PHOTOS_SOCKET": "photos.sock", "ANAS_SHARES_SOCKET": "shares.sock", "ANAS_MEDIA_SOCKET": "media.sock",
		"ANAS_AI_SOCKET": "/run/a-nas-ai/ai.sock", "ANAS_ASSISTANT_TOKEN_DIR": "/etc/a-nas-assistant/tokens",
	}
	_, err := loadConfig(environment)
	if err == nil || !strings.Contains(err.Error(), "ANAS_STATE_DIR must be an absolute path") {
		t.Fatalf("loadConfig() error = %v, want the relative state directory named", err)
	}
	for range 20 {
		if _, again := loadConfig(environment); again == nil || again.Error() != err.Error() {
			t.Fatalf("errors changed order:\n%v\n%v", err, again)
		}
	}
	if _, err := loadConfig(map[string]string{"ANAS_HOSTSTATE_MODE": "fake"}); err == nil || !strings.Contains(err.Error(), "no user configuration directory") {
		t.Fatalf("loadConfig() without a state directory or HOME = %v", err)
	}
}

// The installer writes the only environment the Product Service has on a NAS.
func TestTheInstallersEnvironmentIsAValidNASConfiguration(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "install-v1.0.1-system-services.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// The file's lines, and the optional lines appended after it.
	block := regexp.MustCompile(`printf '%s\\n' \\\n((?:  '[^']*' \\\n)*  '[^']*') > /etc/a-nas/anas-api\.env\n`).FindSubmatch(script)
	if block == nil {
		t.Fatal("the installer no longer writes /etc/a-nas/anas-api.env in one printf")
	}
	environment := map[string]string{}
	for _, line := range regexp.MustCompile(`'(ANAS_[A-Z_]+)=([^']*)'`).FindAllSubmatch(block[1], -1) {
		environment[string(line[1])] = string(line[2])
	}
	for _, line := range regexp.MustCompile(`printf '(ANAS_[A-Z_]+)=([^'\\]*)\\n' >> /etc/a-nas/anas-api\.env`).FindAllSubmatch(script, -1) {
		environment[string(line[1])] = string(line[2])
	}
	config, err := loadConfig(environment)
	if err != nil {
		t.Fatalf("the installer's environment %v does not load: %v", environment, err)
	}
	if !config.Live() || config.Containers != containersAgent || !config.TerminalEnabled || config.MediaWorker != mediaWorkerSocket {
		t.Fatalf("the installer's environment %v gives %+v", environment, config)
	}
}

func TestPhotoServiceConfigNeedsAPositiveQuietPeriod(t *testing.T) {
	config, err := loadPhotoServiceConfig(map[string]string{"ANAS_PHOTOS_AI_QUIET_PERIOD": "2s"})
	if err != nil || config.Quiet.Seconds() != 2 || config.AISocket != "/run/a-nas-ai/ai.sock" || config.Media.client().Socket != "/run/a-nas-media/media.sock" {
		t.Fatalf("loadPhotoServiceConfig() = %+v, %v", config, err)
	}
	for _, value := range []string{"0s", "-1s", "soon"} {
		if _, err := loadPhotoServiceConfig(map[string]string{"ANAS_PHOTOS_AI_QUIET_PERIOD": value}); err == nil {
			t.Errorf("quiet period %q was accepted", value)
		}
	}
}

// The photo and share services name every path that is not absolute, in
// the same order every time, as the Product Service does (#192).
func TestServiceConfigsNameEveryRelativePath(t *testing.T) {
	for _, test := range []struct {
		name string
		load func(map[string]string) error
		env  map[string]string
		want string
	}{
		{"photos", func(e map[string]string) error { _, err := loadPhotoServiceConfig(e); return err },
			map[string]string{"ANAS_PHOTOS_ROOT": "photos", "ANAS_MEDIA_SLOTS": "slots", "ANAS_PHOTOS_AI_QUIET_PERIOD": "soon"},
			"ANAS_MEDIA_SLOTS must be an absolute path\nANAS_PHOTOS_ROOT must be an absolute path\nANAS_PHOTOS_AI_QUIET_PERIOD must be a positive duration such as 5s"},
		{"shares", func(e map[string]string) error { _, err := loadShareServiceConfig(e); return err },
			map[string]string{"ANAS_SHARES_ROOT": "shares", "ANAS_SHARE_SESSION_SOCKET": "sessions.sock"},
			"ANAS_SHARES_ROOT must be an absolute path\nANAS_SHARE_SESSION_SOCKET must be an absolute path"},
	} {
		for range 10 {
			if err := test.load(test.env); err == nil || err.Error() != test.want {
				t.Fatalf("%s: error = %v, want %q", test.name, err, test.want)
			}
		}
	}
	config, err := loadShareServiceConfig(map[string]string{"ANAS_SHARES_ROOT": "/srv/a-nas/data/shares/"})
	if err != nil || config.Root != "/srv/a-nas/data/shares" || config.FilesSocket != "/run/a-nas-share-files/files.sock" {
		t.Fatalf("loadShareServiceConfig() = %+v, %v", config, err)
	}
}

// The vector index is off unless the unit names its socket, which must be an
// absolute path.
func TestPhotoServiceConfigVectorSocket(t *testing.T) {
	if config, err := loadPhotoServiceConfig(map[string]string{}); err != nil || config.VectorSocket != "" {
		t.Fatalf("without the variable: %+v, %v; want the index off", config, err)
	}
	config, err := loadPhotoServiceConfig(map[string]string{"ANAS_PHOTOS_VECTOR_SOCKET": "/run/a-nas-vectors/a-nas-photos.sock"})
	if err != nil || config.VectorSocket != "/run/a-nas-vectors/a-nas-photos.sock" {
		t.Fatalf("loadPhotoServiceConfig() = %+v, %v", config, err)
	}
	if _, err := loadPhotoServiceConfig(map[string]string{"ANAS_PHOTOS_VECTOR_SOCKET": "vectors.sock"}); err == nil {
		t.Error("a relative vector socket was accepted")
	}
}
