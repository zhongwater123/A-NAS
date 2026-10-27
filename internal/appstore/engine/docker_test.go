package engine_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

func TestDockerInspectorReservesStoppedContainerPorts(t *testing.T) {
	for _, state := range []string{"exited", "created", "running"} {
		t.Run(state, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/json"):
					if r.URL.Query().Get("all") != "1" {
						t.Error("must include stopped containers")
					}
					_, _ = fmt.Fprintf(w, `[{"Id":"filedrop","Names":["/filedrop"],"State":%q,"Ports":[]}]`, state)
				case strings.HasSuffix(r.URL.Path, "/containers/filedrop/json"):
					_, _ = fmt.Fprint(w, `{"Id":"filedrop","HostConfig":{"PortBindings":{"8000/tcp":[{"HostIp":"0.0.0.0","HostPort":"8000"}]}}}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = docker.Close() }()
			containers, err := (engine.DockerInspector{Client: docker}).Containers(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(containers) != 1 || !slices.Contains(containers[0].PublishedPorts, uint16(8000)) {
				t.Fatalf("%s container port reservation missing: %+v", state, containers)
			}
			store, _ := newStore(t, &fakeDocker{containers: containers}, &fakeRunner{})
			plan, err := store.Plan(context.Background(), "pyload", identity)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Install(context.Background(), "pyload", plan.Digest, identity); !errors.Is(err, appstore.ErrPortInUse) {
				t.Fatalf("pyLoad must not take FileDrop's port while %s: %v", state, err)
			}
		})
	}
}
