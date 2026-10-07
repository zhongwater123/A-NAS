// Package fake runs the real install engine against an in-memory Docker so
// the App Center can be exercised end to end without a container engine.
package fake

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

// Policy mirrors the production defaults so fake plans show realistic paths.
var Policy = appstore.Policy{
	AppDataRoot:   "/srv/a-nas/data/apps",
	DataRoot:      "/srv/a-nas/data/spaces/shared",
	TZ:            "Asia/Shanghai",
	ReservedPorts: []uint16{8080},
}

// New returns a store whose installs take about step*4 to finish.
func New(step time.Duration) (*engine.Store, error) {
	entries, err := appstore.Catalog()
	if err != nil {
		return nil, err
	}
	state, err := os.MkdirTemp("", "a-nas-fake-apps-")
	if err != nil {
		return nil, err
	}
	docker := &memoryDocker{}
	store := engine.New(entries, Policy, state, docker, &runner{docker: docker, step: step})
	return store, nil
}

type memoryDocker struct {
	mu         sync.Mutex
	containers []engine.ContainerInfo
}

func (m *memoryDocker) Containers(context.Context) ([]engine.ContainerInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]engine.ContainerInfo(nil), m.containers...), nil
}

// runner pretends to pull and start (or stop) the project's services.
type runner struct {
	docker *memoryDocker
	step   time.Duration
}

func (r *runner) Run(ctx context.Context, args []string, output io.Writer) error {
	project, action, file := "", "", ""
	for i, arg := range args {
		if arg == "-p" && i+1 < len(args) {
			project = args[i+1]
		}
		if arg == "-f" && i+1 < len(args) {
			file = args[i+1]
		}
		if arg == "up" || arg == "down" {
			action = arg
		}
	}
	id := strings.TrimPrefix(project, "a-nas-")
	steps := []string{"Pulling images (simulated)", "Creating network " + project + "_default", "Creating containers", "Starting " + id}
	if action == "down" {
		steps = []string{"Stopping " + id, "Removing containers", "Removing network " + project + "_default"}
	}
	for _, line := range steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.step):
		}
		fmt.Fprintln(output, line)
	}

	r.docker.mu.Lock()
	defer r.docker.mu.Unlock()
	kept := r.docker.containers[:0]
	for _, container := range r.docker.containers {
		if container.AppID != id {
			kept = append(kept, container)
		}
	}
	r.docker.containers = kept
	if action == "up" {
		if compose, err := os.ReadFile(file); err == nil && len(compose) > 0 {
			r.docker.containers = append(r.docker.containers, engine.ContainerInfo{Name: filepath.Base(filepath.Dir(file)), AppID: id, Running: true})
		}
	}
	return nil
}
