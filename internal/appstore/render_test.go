package appstore_test

import (
	"context"
	"errors"
	"reflect"
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
`), testPolicy, testIdentity, nil)
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
		"io.a-nas.runtime: \"1\"",
		"no-new-privileges:true",
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
	// It reads Music, so it is in a-nas-users and has no capabilities.
	if !slices.Equal(service.GroupAdd, []string{"20000"}) || !slices.Equal(service.CapDrop, []string{"ALL"}) || !slices.Equal(service.SecurityOpt, []string{"no-new-privileges:true"}) {
		t.Errorf("rendered group_add %v cap_drop %v security_opt %v", service.GroupAdd, service.CapDrop, service.SecurityOpt)
	}
	want := appstore.ServiceRuntime{Service: "web", RunAs: appstore.RunAsApp, User: "30005:30005", Groups: []string{"20000"}, CapDrop: []string{"ALL"}, Shared: appstore.SharedRead}
	if len(plan.Services) != 1 || !reflect.DeepEqual(plan.Services[0], want) {
		t.Errorf("plan services = %+v, want %+v", plan.Services, want)
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
		first, _ := appstore.Render(context.Background(), item, testPolicy, testIdentity, nil)
		second, _ := appstore.Render(context.Background(), item, testPolicy, testIdentity, nil)
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
		"group add":      `group_add: ["20000"]`,
	}
	for name, option := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n    "+option+"\n"), testPolicy, testIdentity, nil)
			var policyError *appstore.PolicyError
			if !errors.As(err, &policyError) {
				t.Fatalf("Render() error = %v, want policy error", err)
			}
		})
	}
}

func TestRenderRecordsTheVolumeAndSubpathOfEachFolder(t *testing.T) {
	plan, err := appstore.Render(context.Background(), entry(`
services:
  web:
    image: example/web:1
    user: "1000"
    volumes:
      - /DATA/AppData/$AppID/config:/config
      - /DATA/Media/Music:/music:ro
      - /etc/localtime:/etc/localtime
`), testPolicy, testIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]string{
		"/config":        {"a-nas-demo_a-nas-appdata", "config"},
		"/music":         {"a-nas-demo_a-nas-shared", "Media/Music"},
		"/etc/localtime": {"", ""},
	}
	for _, mount := range plan.Mounts {
		if got := [2]string{mount.Volume, mount.Subpath}; got != want[mount.ContainerPath] {
			t.Errorf("mount %s = volume %q subpath %q, want %q", mount.ContainerPath, got[0], got[1], want[mount.ContainerPath])
		}
	}
}

func TestRenderListsNetworksAndRefusesOwnAddressRanges(t *testing.T) {
	ctx := context.Background()
	plan, err := appstore.Render(ctx, entry("services:\n  web:\n    image: example/web:1\n"), testPolicy, testIdentity, nil)
	if err != nil || !slices.Equal(plan.Networks, []string{"default"}) {
		t.Fatalf("implicit network = %v, %v; want the Compose default network", plan.Networks, err)
	}
	plan, err = appstore.Render(ctx, entry("services:\n  web:\n    image: example/web:1\n    network_mode: bridge\n"), testPolicy, testIdentity, nil)
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
`), testPolicy, testIdentity, nil)
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
`), testPolicy, testIdentity, nil)
	var policyError *appstore.PolicyError
	if !errors.As(err, &policyError) {
		t.Fatalf("Render() error = %v, want policy error", err)
	}
}

func TestRenderRequiresAnAppIdentity(t *testing.T) {
	_, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n"), testPolicy, appstore.Identity{}, nil)
	var policyError *appstore.PolicyError
	if !errors.As(err, &policyError) {
		t.Fatalf("Render() without identity error = %v, want policy error", err)
	}
}

func TestRenderKeepsSharedMountsInsideTheSharedFolder(t *testing.T) {
	plan, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n    user: \"1000\"\n    volumes: [\"/DATA:/data\"]\n"), testPolicy, testIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Mounts[0].HostPath; got != "/srv/a-nas/data/spaces/shared" {
		t.Fatalf("/DATA maps to %s, want the Shared folder rather than the volume root", got)
	}
}

// A Shared folder is read-only unless the app is listed as writing it; the
// demo app is not, qBittorrent is.
func TestRenderMountsSharedReadOnlyUnlessTheAppWritesIt(t *testing.T) {
	plan, err := appstore.Render(context.Background(), entry(`
services:
  web:
    image: example/web:1
    user: "1000"
    volumes: ["/DATA/Downloads:/downloads"]
`), testPolicy, testIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Mounts[0].ReadOnly || !strings.Contains(string(plan.Compose), "read_only: true") || plan.Services[0].Shared != appstore.SharedRead {
		t.Fatalf("mount = %+v, service = %+v:\n%s", plan.Mounts[0], plan.Services[0], plan.Compose)
	}

	catalog, err := appstore.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(catalog, func(entry appstore.Entry) bool { return entry.App.ID == "qbittorrent" })
	plan, err = appstore.Render(context.Background(), catalog[index], testPolicy, testIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, mount := range plan.Mounts {
		if mount.Kind == appstore.MountShared && mount.ReadOnly {
			t.Errorf("qBittorrent gets %s read-only", mount.ContainerPath)
		}
	}
	if plan.Services[0].Shared != appstore.SharedWrite {
		t.Errorf("qBittorrent Shared access = %q", plan.Services[0].Shared)
	}
}

// A service that keeps the image's user may run as root, which ignores the
// Shared ACL; it may only mount its own app data.
func TestRenderRefusesSharedForServicesThatKeepTheImageUser(t *testing.T) {
	ctx := context.Background()
	_, err := appstore.Render(ctx, entry(`
services:
  web:
    image: example/web:1
    volumes: ["/DATA/Media:/media"]
`), testPolicy, testIdentity, nil)
	var policyError *appstore.PolicyError
	if !errors.As(err, &policyError) || !strings.Contains(err.Error(), "keeps its image's user") {
		t.Fatalf("Render() error = %v, want the Shared mount refused", err)
	}
	plan, err := appstore.Render(ctx, entry(`
services:
  web:
    image: example/web:1
    volumes: ["/DATA/AppData/$AppID/data:/data"]
`), testPolicy, testIdentity, nil)
	if err != nil {
		t.Fatal(err)
	}
	service := plan.Services[0]
	if service.RunAs != appstore.RunAsImage || service.User != "" || len(service.Groups) > 0 || !strings.Contains(string(plan.Compose), "no-new-privileges:true") {
		t.Fatalf("image-user service = %+v:\n%s", service, plan.Compose)
	}
}

// Images that switch to PUID/PGID after starting as root drop the groups
// Docker adds, so a-nas-users becomes the group they switch to; without
// Shared they switch to the app group.
func TestRenderSwitchesPUIDImagesToTheAppIdentity(t *testing.T) {
	for _, test := range []struct {
		volume, pgid string
		shared       appstore.SharedAccess
	}{
		{"/DATA/Downloads:/downloads", "20000", appstore.SharedRead},
		{"/DATA/AppData/$AppID/config:/config", "30005", appstore.SharedNone},
	} {
		plan, err := appstore.Render(context.Background(), entry(`
services:
  web:
    image: example/web:1
    environment:
      PUID: "1000"
    volumes: ["`+test.volume+`"]
`), testPolicy, testIdentity, nil)
		if err != nil {
			t.Fatal(err)
		}
		compose := string(plan.Compose)
		service := plan.Services[0]
		if service.RunAs != appstore.RunAsAppAfterInit || service.Shared != test.shared || service.User != "" ||
			!strings.Contains(compose, `PUID: "30005"`) || !strings.Contains(compose, `PGID: "`+test.pgid+`"`) || strings.Contains(compose, "group_add") {
			t.Errorf("%s: service = %+v:\n%s", test.volume, service, compose)
		}
	}
}
