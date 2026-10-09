package appstore_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/zhongwater123/A-NAS/internal/appstore"
)

func entry(compose string) appstore.Entry {
	return appstore.Entry{App: appstore.App{ID: "demo", Title: "Demo"}, Compose: []byte(compose)}
}

func TestRenderRewritesPathsAndLabelsServices(t *testing.T) {
	plan, err := appstore.Render(context.Background(), entry(`
name: demo
services:
  web:
    image: example/web:1
    user: "999:1000"
    ports:
      - target: 80
        published: "8099"
    volumes:
      - type: bind
        source: /DATA/AppData/$AppID/config
        target: /config
      - /DATA/Media/Music:/music:ro
      - /etc/localtime:/etc/localtime
      - /etc/timezone:/etc/timezone:ro
    environment:
      TZ: $TZ
      PUID: "1000"
      PGID: "1000"
x-casaos:
  title:
    en_US: Demo
`), testPolicy, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	compose := string(plan.Compose)
	for _, want := range []string{
		"device: /srv/a-nas/data/apps/demo",
		"device: /srv/a-nas/data/spaces/shared",
		"subpath: config",
		"subpath: Media/Music",
		"PUID: \"30005\"",
		"PGID: \"30005\"",
		"io.a-nas.app: demo",
		"TZ: Asia/Shanghai",
		"name: a-nas-demo",
		"user: 30005:30005",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("rendered compose lacks %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "/DATA") || strings.Contains(compose, "/etc/timezone") || strings.Contains(compose, "x-casaos") {
		t.Errorf("rendered compose keeps CasaOS-only content:\n%s", compose)
	}
	// The Host Agent creates every mounted folder; Docker must never create
	// one, or an offline data volume would put app data on the system disk.
	if strings.Contains(compose, "create_host_path: true") {
		t.Errorf("rendered compose lets Docker create host folders:\n%s", compose)
	}
	reloaded, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
		WorkingDir:  "/",
		ConfigFiles: []types.ConfigFile{{Filename: "/docker-compose.yml", Content: plan.Compose}},
	}, func(options *loader.Options) { options.SetProjectName("a-nas-demo", true) })
	if err != nil {
		t.Fatalf("rendered compose does not load: %v", err)
	}
	service := reloaded.Services["web"]
	if service.User != "30005:30005" {
		t.Errorf("rendered user = %q, want app identity", service.User)
	}
	for name := range map[string]bool{"PUID": true, "PGID": true} {
		value := service.Environment[name]
		if value == nil || *value != "30005" {
			t.Errorf("rendered %s = %v, want app identity", name, value)
		}
	}
	// Data-volume folders are subpaths of A-NAS volumes, which Docker resolves
	// without following links out of them; only /etc/localtime stays a bind.
	for _, volume := range service.Volumes {
		switch {
		case volume.Type == types.VolumeTypeBind && volume.Source != "/etc/localtime":
			t.Errorf("%s is a bind mount", volume.Source)
		case volume.Bind != nil && bool(volume.Bind.CreateHostPath):
			t.Errorf("%s create_host_path = true", volume.Source)
		case volume.Type == types.VolumeTypeVolume && (volume.Volume == nil || !volume.Volume.NoCopy || volume.Volume.Subpath == ""):
			t.Errorf("%s is not a no-copy subpath: %+v", volume.Source, volume.Volume)
		}
	}
	for name, device := range map[string]string{"a-nas-appdata": "/srv/a-nas/data/apps/demo", "a-nas-shared": "/srv/a-nas/data/spaces/shared"} {
		volume := reloaded.Volumes[name]
		if volume.Driver != "local" || volume.DriverOpts["o"] != "bind" || volume.DriverOpts["device"] != device {
			t.Errorf("volume %s = %+v, want a bind of %s", name, volume, device)
		}
	}
	if got := []string{plan.Mounts[0].HostPath, plan.Mounts[1].HostPath}; got[0] != "/srv/a-nas/data/apps/demo/config" || got[1] != "/srv/a-nas/data/spaces/shared/Media/Music" {
		t.Errorf("plan host paths = %v", got)
	}
	if plan.Identity != testIdentity {
		t.Errorf("plan identity = %+v, want %+v", plan.Identity, testIdentity)
	}
	if !plan.SharesFolders() || len(plan.HostFolders()) != 2 {
		t.Errorf("shared = %v, host folders = %v", plan.SharesFolders(), plan.HostFolders())
	}
	if len(plan.Mounts) != 3 || plan.Mounts[2].Kind != appstore.MountSystem || !plan.Mounts[2].ReadOnly {
		t.Errorf("mounts = %+v", plan.Mounts)
	}
	if len(plan.Ports) != 1 || plan.Ports[0].HostPort != 8099 || plan.Ports[0].ContainerPort != 80 || len(plan.Digest) != 64 {
		t.Errorf("plan = %+v", plan)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	entries, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range entries[:3] {
		first, _ := appstore.Render(context.Background(), item, testPolicy, testIdentity)
		second, _ := appstore.Render(context.Background(), item, testPolicy, testIdentity)
		if first.Digest == "" || first.Digest != second.Digest {
			t.Fatalf("%s digest is not stable: %s vs %s", item.App.ID, first.Digest, second.Digest)
		}
	}
}

func TestRenderRejectsHostPrivileges(t *testing.T) {
	tests := map[string]string{
		"privileged":     `privileged: true`,
		"docker socket":  `volumes: ["/var/run/docker.sock:/var/run/docker.sock"]`,
		"api socket":     `use_api_socket: true`,
		"host network":   `network_mode: host`,
		"host pid":       `pid: host`,
		"devices":        `devices: ["/dev/dri:/dev/dri"]`,
		"capabilities":   `cap_add: [NET_ADMIN]`,
		"low port":       `ports: ["80:80"]`,
		"reserved port":  `ports: ["8080:80"]`,
		"low port range": `ports: ["1000-1025:1000-1025"]`,
		"escape":         `volumes: ["/DATA/../etc:/host-etc"]`,
		"other app data": `volumes: ["/DATA/AppData/other:/data"]`,
		"relative path":  `volumes: ["./data:/data"]`,
		"env file":       `env_file: /etc/hostname`,
		"build":          `build: .`,
		"security opt":   `security_opt: ["seccomp:unconfined"]`,
	}
	for name, option := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n    "+option+"\n"), testPolicy, testIdentity)
			var policyError *appstore.PolicyError
			if !errors.As(err, &policyError) {
				t.Fatalf("Render() error = %v, want policy error", err)
			}
		})
	}
}

func TestRenderListsNetworksAndRefusesOwnAddressRanges(t *testing.T) {
	ctx := context.Background()
	plan, err := appstore.Render(ctx, entry("services:\n  web:\n    image: example/web:1\n"), testPolicy, testIdentity)
	if err != nil || !slices.Equal(plan.Networks, []string{"default"}) {
		t.Fatalf("implicit network = %v, %v; want the Compose default network", plan.Networks, err)
	}
	plan, err = appstore.Render(ctx, entry("services:\n  web:\n    image: example/web:1\n    network_mode: bridge\n"), testPolicy, testIdentity)
	if err != nil || len(plan.Networks) != 0 {
		t.Fatalf("default bridge = %v, %v; want no networks", plan.Networks, err)
	}

	_, err = appstore.Render(ctx, entry(`
services:
  web:
    image: example/web:1
    networks: [app]
networks:
  app:
    ipam:
      config:
        - subnet: 172.19.0.0/16
`), testPolicy, testIdentity)
	var policyError *appstore.PolicyError
	if !errors.As(err, &policyError) || !strings.Contains(err.Error(), "network app sets its own address range") {
		t.Fatalf("own subnet error = %v", err)
	}
}

func TestRenderRefusesReservedVolumeNames(t *testing.T) {
	// A manifest volume named like the Shared volume would reach Shared
	// without the plan listing it.
	_, err := appstore.Render(context.Background(), entry(`
services:
  web:
    image: example/web:1
    volumes: ["a-nas-shared:/data"]
volumes:
  a-nas-shared: {}
`), testPolicy, testIdentity)
	var policyError *appstore.PolicyError
	if !errors.As(err, &policyError) {
		t.Fatalf("Render() error = %v, want policy error", err)
	}
}

func TestRenderRequiresAnAppIdentity(t *testing.T) {
	_, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n"), testPolicy, appstore.Identity{})
	var policyError *appstore.PolicyError
	if !errors.As(err, &policyError) {
		t.Fatalf("Render() without identity error = %v, want policy error", err)
	}
}

func TestRenderKeepsSharedMountsInsideTheSharedFolder(t *testing.T) {
	plan, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n    volumes: [\"/DATA:/data\"]\n"), testPolicy, testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Mounts[0].HostPath; got != "/srv/a-nas/data/spaces/shared" {
		t.Fatalf("/DATA maps to %s, want the Shared folder rather than the volume root", got)
	}
}
