// Package enginetest holds the contract of an engine.Inspector: what Docker
// reports for a Compose project that the engine's Runner started from a
// plan, as the install checks read it. The in-memory Docker of
// internal/appstore/fake and the real Docker (the dockerintegration build
// tag) run the same table, so the fake cannot drift from what the checks
// expect of Docker (#194).
package enginetest

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zhongwater123/A-NAS/internal/appstore"
	"github.com/zhongwater123/A-NAS/internal/appstore/engine"
)

// Check is one report of an Inspector compared with the plan it ran.
type Check struct {
	Name  string
	Check func(context.Context, engine.Inspector, appstore.Plan) error
}

// Checks is the contract.
var Checks = []Check{
	{"every planned container is listed with its app, runtime rules and published ports", checkContainers},
	{"the project's containers run their planned services", checkProjectContainers},
	{"each planned folder is mounted from its volume and subpath, read-only as planned", checkMounts},
	{"each container runs as its service is planned", checkRuntimes},
	{"the project's networks are the planned ones, inside the address pools", checkNetworks},
}

// CheckProject starts plan with runner as an install does and checks every
// report of inspector against it. When the test ends, it takes the project
// down again and checks that inspector reports nothing of it any more.
func CheckProject(t *testing.T, inspector engine.Inspector, runner engine.Runner, plan appstore.Plan) {
	t.Helper()
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(file, plan.Compose, 0o600); err != nil {
		t.Fatal(err)
	}
	compose := []string{"compose", "--progress", "plain", "-p", plan.Project, "-f", file}
	t.Cleanup(func() {
		if err := runner.Run(ctx, append(compose, "down", "--remove-orphans", "--volumes"), io.Discard); err != nil {
			t.Errorf("compose down: %v", err)
			return
		}
		if err := gone(ctx, inspector, plan); err != nil {
			t.Error(err)
		}
	})
	if err := runner.Run(ctx, append(compose, "up", "--detach", "--remove-orphans"), io.Discard); err != nil {
		t.Fatalf("compose up: %v", err)
	}
	for _, check := range Checks {
		if err := check.Check(ctx, inspector, plan); err != nil {
			t.Errorf("%s: %v", check.Name, err)
		}
	}
}

func checkContainers(ctx context.Context, inspector engine.Inspector, plan appstore.Plan) error {
	containers, err := inspector.Containers(ctx)
	if err != nil {
		return err
	}
	var published []uint16
	for _, name := range plan.Containers {
		i := slices.IndexFunc(containers, func(c engine.ContainerInfo) bool { return c.Name == name })
		if i < 0 {
			return fmt.Errorf("container %s is not listed", name)
		}
		container := containers[i]
		if container.AppID != plan.AppID || container.Runtime != appstore.RuntimePolicy || !container.Running {
			return fmt.Errorf("container %s: app %q, runtime %q, running %v", name, container.AppID, container.Runtime, container.Running)
		}
		published = append(published, container.PublishedPorts...)
	}
	for _, port := range plan.Ports {
		if !slices.Contains(published, port.HostPort) {
			return fmt.Errorf("port %d is not published", port.HostPort)
		}
	}
	return nil
}

func checkProjectContainers(ctx context.Context, inspector engine.Inspector, plan appstore.Plan) error {
	states, err := inspector.ProjectContainers(ctx, plan.Project)
	if err != nil {
		return err
	}
	var services []string
	for _, state := range states {
		if state.State != "running" {
			return fmt.Errorf("service %s is %s", state.Service, state.State)
		}
		services = append(services, state.Service)
	}
	if want := plannedServices(plan); !sameSet(services, want) {
		return fmt.Errorf("services %v, want %v", services, want)
	}
	return nil
}

func checkMounts(ctx context.Context, inspector engine.Inspector, plan appstore.Plan) error {
	mounts, err := inspector.ProjectMounts(ctx, plan.Project)
	if err != nil {
		return err
	}
	for _, want := range plan.Mounts {
		if want.Volume == "" {
			continue
		}
		if !slices.ContainsFunc(mounts, func(got engine.MountInfo) bool {
			return got.Volume == want.Volume && got.Target == want.ContainerPath && got.Subpath == want.Subpath && got.ReadOnly == want.ReadOnly
		}) {
			return fmt.Errorf("no container mounts %s/%s at %s (read-only %v); mounts %+v", want.Volume, want.Subpath, want.ContainerPath, want.ReadOnly, mounts)
		}
	}
	return nil
}

func checkRuntimes(ctx context.Context, inspector engine.Inspector, plan appstore.Plan) error {
	runtimes, err := inspector.ProjectRuntimes(ctx, plan.Project)
	if err != nil {
		return err
	}
	for _, service := range plan.Services {
		i := slices.IndexFunc(runtimes, func(got engine.RuntimeInfo) bool { return got.Service == service.Service })
		if i < 0 {
			return fmt.Errorf("service %s has no container", service.Service)
		}
		got := runtimes[i]
		switch {
		case service.RunAs == appstore.RunAsApp && got.User != service.User:
			return fmt.Errorf("%s runs as %q, want %q", got.Container, got.User, service.User)
		case !sameSet(got.GroupAdd, service.Groups):
			return fmt.Errorf("%s has groups %v, want %v", got.Container, got.GroupAdd, service.Groups)
		case !slices.ContainsFunc(got.SecurityOpt, func(option string) bool { return strings.HasPrefix(option, "no-new-privileges") }):
			return fmt.Errorf("%s may gain privileges: %v", got.Container, got.SecurityOpt)
		}
		for _, capability := range service.CapDrop {
			if !slices.ContainsFunc(got.CapDrop, func(dropped string) bool {
				return strings.TrimPrefix(strings.ToUpper(dropped), "CAP_") == strings.TrimPrefix(strings.ToUpper(capability), "CAP_")
			}) {
				return fmt.Errorf("%s keeps %s: drops %v", got.Container, capability, got.CapDrop)
			}
		}
	}
	return nil
}

func checkNetworks(ctx context.Context, inspector engine.Inspector, plan appstore.Plan) error {
	networks, err := inspector.ProjectNetworks(ctx, plan.Project)
	if err != nil {
		return err
	}
	if len(networks) != len(plan.Networks) {
		return fmt.Errorf("networks %+v, want %v", networks, plan.Networks)
	}
	for _, network := range networks {
		for _, subnet := range network.Subnets {
			if len(plan.AddressPools) > 0 && !slices.ContainsFunc(plan.AddressPools, func(pool netip.Prefix) bool {
				return pool.Bits() <= subnet.Bits() && pool.Contains(subnet.Masked().Addr())
			}) {
				return fmt.Errorf("network %s has %s, outside the address pools %v", network.Name, subnet, plan.AddressPools)
			}
		}
	}
	return nil
}

// gone reports anything inspector still knows of the project.
func gone(ctx context.Context, inspector engine.Inspector, plan appstore.Plan) error {
	containers, err := inspector.Containers(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(containers, func(c engine.ContainerInfo) bool { return c.AppID == plan.AppID }) {
		return fmt.Errorf("containers of %s remain after down", plan.AppID)
	}
	states, err := inspector.ProjectContainers(ctx, plan.Project)
	if err != nil || len(states) > 0 {
		return fmt.Errorf("project containers after down: %v, %v", states, err)
	}
	networks, err := inspector.ProjectNetworks(ctx, plan.Project)
	if err != nil || len(networks) > 0 {
		return fmt.Errorf("project networks after down: %v, %v", networks, err)
	}
	return nil
}

func plannedServices(plan appstore.Plan) []string {
	services := make([]string, 0, len(plan.Services))
	for _, service := range plan.Services {
		services = append(services, service.Service)
	}
	return services
}

func sameSet(got, want []string) bool {
	return len(got) == len(want) && !slices.ContainsFunc(want, func(item string) bool { return !slices.Contains(got, item) })
}
