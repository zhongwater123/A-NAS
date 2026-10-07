package appstore_test

import (
	"context"
	"errors"
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
x-casaos:
  title:
    en_US: Demo
`), testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	compose := string(plan.Compose)
	for _, want := range []string{
		"source: /srv/a-nas/appdata/demo/config",
		"source: /srv/a-nas/data/Media/Music",
		"io.a-nas.app: demo",
		"TZ: Asia/Shanghai",
		"name: a-nas-demo",
	} {
		if !strings.Contains(compose, want) {
			t.Errorf("rendered compose lacks %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "/DATA") || strings.Contains(compose, "/etc/timezone") || strings.Contains(compose, "x-casaos") {
		t.Errorf("rendered compose keeps CasaOS-only content:\n%s", compose)
	}
	// Docker Compose 2.40 treats an absent create_host_path in long syntax as
	// false and refuses to start; newer compose-go defaults it to true, so the
	// field must be written out rather than left to either default.
	if strings.Count(compose, "create_host_path: true") != 2 || strings.Count(compose, "create_host_path: false") != 1 {
		t.Errorf("writable binds must set create_host_path explicitly:\n%s", compose)
	}
	reloaded, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
		WorkingDir:  "/",
		ConfigFiles: []types.ConfigFile{{Filename: "/docker-compose.yml", Content: plan.Compose}},
	}, func(options *loader.Options) { options.SetProjectName("a-nas-demo", true) })
	if err != nil {
		t.Fatalf("rendered compose does not load: %v", err)
	}
	for _, volume := range reloaded.Services["web"].Volumes {
		want := volume.Source != "/etc/localtime"
		if volume.Bind == nil || bool(volume.Bind.CreateHostPath) != want {
			t.Errorf("%s create_host_path = %v, want %v", volume.Source, volume.Bind, want)
		}
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
		first, _ := appstore.Render(context.Background(), item, testPolicy)
		second, _ := appstore.Render(context.Background(), item, testPolicy)
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
			_, err := appstore.Render(context.Background(), entry("services:\n  web:\n    image: example/web:1\n    "+option+"\n"), testPolicy)
			var policyError *appstore.PolicyError
			if !errors.As(err, &policyError) {
				t.Fatalf("Render() error = %v, want policy error", err)
			}
		})
	}
}
