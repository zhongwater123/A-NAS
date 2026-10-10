package appstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

// casaosDataRoot is where CasaOS manifests place host data; A-NAS rewrites it.
const casaosDataRoot = "/DATA"

// projectDir is a non-existent working directory, so relative paths in a
// manifest resolve outside the allowed roots and are rejected.
const projectDir = "/nonexistent/a-nas-app"

// Folders on the data volume are mounted through these volumes, never as
// bind mounts; manifests cannot declare volumes with the prefix.
const (
	reservedVolumePrefix = "a-nas-"
	appDataVolume        = "a-nas-appdata"
	sharedVolume         = "a-nas-shared"
)

// Policy holds the host facts a plan is rendered against.
type Policy struct {
	// AppDataRoot receives /DATA/AppData/<app>/...; each app gets its own
	// subdirectory on the data volume that SMB does not publish.
	AppDataRoot string
	// DataRoot receives every other /DATA/... path. It is the Shared folder,
	// never the data-volume root, so an app cannot reach private spaces.
	DataRoot string
	TZ       string
	// ReservedPorts are host ports A-NAS itself needs.
	ReservedPorts []uint16
}

type MountKind string

const (
	MountAppData MountKind = "appdata"
	MountShared  MountKind = "shared"
	// MountSystem is a read-only host file such as /etc/localtime.
	MountSystem MountKind = "system"
)

type Mount struct {
	HostPath      string
	ContainerPath string
	Kind          MountKind
	ReadOnly      bool
	Purpose       string
	// Volume and Subpath say how Docker must mount an app-data or Shared
	// folder: the Docker volume rooted at the A-NAS folder and the path below
	// it. Both are empty for read-only system files.
	Volume  string
	Subpath string
}

type PortMapping struct {
	HostPort      uint16
	ContainerPort uint16
	Protocol      string
	Purpose       string
}

// Plan is what the owner confirms before an install: the identity the app
// runs as, images that will be pulled, host ports that will open, host
// folders containers can write and Docker networks the app creates.
type Plan struct {
	AppID      string
	Title      string
	Version    string
	Project    string
	Identity   Identity
	Images     []string
	Containers []string
	Ports      []PortMapping
	Mounts     []Mount
	// Networks are the Compose networks the install creates, including the
	// implicit default network; empty when every service uses Docker's
	// default bridge.
	Networks []string
	// AddressPools are the Docker address pools those networks come from. The
	// engine fills them in; Render does not know the daemon.
	AddressPools []netip.Prefix
	Digest       string
	Compose      []byte
}

// PolicyError lists every reason a manifest cannot be installed.
type PolicyError struct {
	Reasons []string
}

func (e *PolicyError) Error() string {
	return "manifest violates install policy: " + strings.Join(e.Reasons, "; ")
}

// Render validates a manifest against policy and produces the Compose file
// that will run as identity, rewritten to A-NAS paths and labelled with the
// app ID.
func Render(ctx context.Context, entry Entry, policy Policy, identity Identity) (Plan, error) {
	if identity.UID <= 0 || identity.GID <= 0 {
		return Plan{}, &PolicyError{Reasons: []string{"app identity is missing"}}
	}
	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir:  projectDir,
		ConfigFiles: []types.ConfigFile{{Filename: path.Join(projectDir, "docker-compose.yml"), Content: entry.Compose}},
		Environment: types.Mapping{
			"AppID": entry.App.ID, "TZ": policy.TZ,
			"PUID": strconv.Itoa(identity.UID), "PGID": strconv.Itoa(identity.GID),
		},
	}, func(options *loader.Options) {
		options.SetProjectName(ProjectName(entry.App.ID), true)
		options.SkipResolveEnvironment = true
		options.ResolvePaths = true
	})
	if err != nil {
		return Plan{}, &PolicyError{Reasons: []string{"cannot load manifest: " + err.Error()}}
	}

	var reasons []string
	reject := func(format string, args ...any) { reasons = append(reasons, fmt.Sprintf(format, args...)) }
	reserved := make(map[uint16]bool, len(policy.ReservedPorts))
	for _, port := range policy.ReservedPorts {
		reserved[port] = true
	}

	for name, volume := range project.Volumes {
		if bool(volume.External) || volume.Driver != "" || len(volume.DriverOpts) > 0 {
			reject("volume %s uses an external or custom driver", name)
		}
		if strings.HasPrefix(name, reservedVolumePrefix) {
			reject("volume %s uses a name A-NAS reserves", name)
		}
	}
	for name, network := range project.Networks {
		if bool(network.External) || (network.Driver != "" && network.Driver != "bridge") {
			reject("network %s is external or not a bridge", name)
		}
		// Addresses come only from the A-NAS address pool (ADR 0013).
		if network.Ipam.Driver != "" || len(network.Ipam.Config) > 0 {
			reject("network %s sets its own address range", name)
		}
	}
	for name, config := range project.Configs {
		if config.File != "" {
			reject("config %s reads a host file", name)
		}
	}
	for name, secret := range project.Secrets {
		if secret.File != "" {
			reject("secret %s reads a host file", name)
		}
	}

	plan := Plan{AppID: entry.App.ID, Title: entry.App.Title, Version: entry.App.Version, Project: project.Name, Identity: identity}
	for name := range project.Networks {
		plan.Networks = append(plan.Networks, name)
	}
	sort.Strings(plan.Networks)
	// Volume name to the folder it is rooted at.
	bases := map[string]string{}
	serviceNames := make([]string, 0, len(project.Services))
	for name := range project.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	for _, name := range serviceNames {
		service := project.Services[name]
		descriptions := casaosDescriptions(service.Extensions)
		checkService(name, service, reject)
		applyAppIdentity(&service, identity)

		volumes := make([]types.ServiceVolumeConfig, 0, len(service.Volumes))
		for _, volume := range service.Volumes {
			switch volume.Type {
			case types.VolumeTypeBind:
				switch path.Clean(volume.Source) {
				case "/etc/localtime":
					// Time zone data is safe to share, but only read-only and never created.
					volume.ReadOnly = true
					volume.Bind = &types.ServiceVolumeBind{}
					plan.Mounts = append(plan.Mounts, Mount{HostPath: "/etc/localtime", ContainerPath: volume.Target, Kind: MountSystem, ReadOnly: true, Purpose: descriptions.volumes[volume.Target]})
					volumes = append(volumes, volume)
					continue
				case "/etc/timezone":
					// Absent on current Debian; services receive TZ instead.
					continue
				}
				base, subpath, kind, ok := rewriteHostPath(volume.Source, entry.App.ID, policy)
				if !ok {
					reject("service %s mounts %s, which is outside %s", name, volume.Source, casaosDataRoot)
					continue
				}
				// Mount through a volume rooted at base, which only root can
				// rename, with the rest as a subpath. Docker resolves a subpath on
				// every start without following links out of the volume; a bind
				// source would be followed, and anyone who can write the Shared
				// folder or the app's data could swap a folder for a link to the
				// host. Docker creates neither base nor subpath: the Host Agent
				// creates the folders, and an offline data volume keeps the app
				// stopped instead of putting its data on the system disk.
				source := sharedVolume
				if kind == MountAppData {
					source = appDataVolume
				}
				bases[source] = base
				volume.Type = types.VolumeTypeVolume
				volume.Source = source
				volume.Bind = nil
				volume.Volume = &types.ServiceVolumeVolume{NoCopy: true, Subpath: subpath}
				plan.Mounts = append(plan.Mounts, Mount{
					HostPath: path.Join(base, subpath), ContainerPath: volume.Target, Kind: kind, ReadOnly: volume.ReadOnly,
					Purpose: descriptions.volumes[volume.Target], Volume: project.Name + "_" + source, Subpath: subpath,
				})
			case types.VolumeTypeVolume, types.VolumeTypeTmpfs:
			default:
				reject("service %s uses a %s mount", name, volume.Type)
			}
			volumes = append(volumes, volume)
		}
		service.Volumes = volumes

		for _, port := range service.Ports {
			published, err := strconv.ParseUint(port.Published, 10, 16)
			switch {
			case err != nil:
				reject("service %s publishes %q, which is not a single port", name, port.Published)
				continue
			case published < 1024:
				reject("service %s publishes privileged port %d", name, published)
				continue
			case reserved[uint16(published)]:
				reject("service %s publishes port %d, which A-NAS reserves", name, published)
				continue
			}
			protocol := port.Protocol
			if protocol == "" {
				protocol = "tcp"
			}
			plan.Ports = append(plan.Ports, PortMapping{
				HostPort: uint16(published), ContainerPort: uint16(port.Target), Protocol: protocol,
				Purpose: descriptions.ports[strconv.FormatUint(uint64(port.Target), 10)],
			})
		}

		if service.Labels == nil {
			service.Labels = types.Labels{}
		}
		service.Labels[AppLabel] = entry.App.ID
		service.Extensions = nil
		project.Services[name] = service

		plan.Images = appendUnique(plan.Images, service.Image)
		container := service.ContainerName
		if container == "" {
			container = fmt.Sprintf("%s-%s-1", project.Name, name)
		}
		plan.Containers = append(plan.Containers, container)
	}
	if len(project.Services) == 0 {
		reject("manifest defines no services")
	}
	if len(reasons) > 0 {
		return Plan{}, &PolicyError{Reasons: reasons}
	}

	for name, base := range bases {
		if project.Volumes == nil {
			project.Volumes = types.Volumes{}
		}
		project.Volumes[name] = types.VolumeConfig{
			Name:       project.Name + "_" + name,
			Driver:     "local",
			DriverOpts: map[string]string{"type": "none", "o": "bind", "device": base},
		}
	}
	project.Extensions = nil
	rendered, err := project.MarshalYAML()
	if err != nil {
		return Plan{}, fmt.Errorf("render compose file: %w", err)
	}
	sum := sha256.Sum256(rendered)
	plan.Digest = hex.EncodeToString(sum[:])
	plan.Compose = rendered
	sort.Slice(plan.Ports, func(i, j int) bool { return plan.Ports[i].HostPort < plan.Ports[j].HostPort })
	return plan, nil
}

// applyAppIdentity adapts the two runtime-identity conventions accepted by
// the reviewed catalog to the stable Linux identity allocated by the Host
// Agent. An explicit Compose user overrides the image user, while PUID/PGID
// are image-specific environment conventions; literal upstream values must
// not escape the A-NAS identity boundary in either form.
func applyAppIdentity(service *types.ServiceConfig, identity Identity) {
	uid := strconv.Itoa(identity.UID)
	gid := strconv.Itoa(identity.GID)
	if strings.TrimSpace(service.User) != "" {
		service.User = uid + ":" + gid
	}
	for name, value := range map[string]string{"PUID": uid, "PGID": gid} {
		if _, declared := service.Environment[name]; declared {
			value := value
			service.Environment[name] = &value
		}
	}
}

func checkService(name string, service types.ServiceConfig, reject func(string, ...any)) {
	if service.Build != nil {
		reject("service %s builds an image", name)
	}
	if strings.TrimSpace(service.Image) == "" {
		reject("service %s has no image", name)
	}
	if service.Privileged {
		reject("service %s is privileged", name)
	}
	if service.UseAPISocket {
		reject("service %s requests the Docker API socket", name)
	}
	for option, set := range map[string]bool{
		"cap_add":          len(service.CapAdd) > 0,
		"devices":          len(service.Devices) > 0 || len(service.DeviceCgroupRules) > 0,
		"gpus":             len(service.Gpus) > 0 || deployDevices(service.Deploy),
		"security_opt":     len(service.SecurityOpt) > 0,
		"sysctls":          len(service.Sysctls) > 0,
		"pid":              service.Pid != "",
		"ipc":              service.Ipc != "",
		"uts":              service.Uts != "",
		"userns_mode":      service.UserNSMode != "",
		"cgroup":           service.Cgroup != "" || service.CgroupParent != "",
		"runtime":          service.Runtime != "",
		"volumes_from":     len(service.VolumesFrom) > 0,
		"env_file":         len(service.EnvFiles) > 0 || len(service.LabelFiles) > 0,
		"network_mode":     service.NetworkMode != "" && service.NetworkMode != "bridge" && service.NetworkMode != "default",
		"extends":          service.Extends != nil,
		"lifecycle hooks":  len(service.PostStart) > 0 || len(service.PreStop) > 0 || len(service.PreStart) > 0,
		"provider":         service.Provider != nil,
		"storage_opt":      len(service.StorageOpt) > 0,
		"credential_spec":  service.CredentialSpec != nil,
		"oom_kill_disable": service.OomKillDisable,
		"external_links":   len(service.ExternalLinks) > 0,
	} {
		if set {
			reject("service %s sets %s", name, option)
		}
	}
}

func deployDevices(deploy *types.DeployConfig) bool {
	if deploy == nil {
		return false
	}
	for _, resource := range []*types.Resource{deploy.Resources.Limits, deploy.Resources.Reservations} {
		if resource != nil && (len(resource.Devices) > 0 || len(resource.GenericResources) > 0) {
			return true
		}
	}
	return false
}

// rewriteHostPath maps a CasaOS /DATA path to an A-NAS root and the relative
// path below it, refusing anything that escapes them after cleaning.
func rewriteHostPath(source, appID string, policy Policy) (base, subpath string, kind MountKind, ok bool) {
	cleaned := path.Clean(source)
	appData := path.Join(casaosDataRoot, "AppData", appID)
	below := func(root string) string { return strings.TrimPrefix(strings.TrimPrefix(cleaned, root), "/") }
	switch {
	case cleaned == appData || strings.HasPrefix(cleaned, appData+"/"):
		return path.Join(policy.AppDataRoot, appID), below(appData), MountAppData, true
	case strings.HasPrefix(cleaned, path.Join(casaosDataRoot, "AppData")+"/"):
		// Another app's data folder.
		return "", "", "", false
	case cleaned == casaosDataRoot || strings.HasPrefix(cleaned, casaosDataRoot+"/"):
		return policy.DataRoot, below(casaosDataRoot), MountShared, true
	default:
		return "", "", "", false
	}
}

type serviceDescriptions struct {
	ports   map[string]string
	volumes map[string]string
}

// casaosDescriptions reads the x-casaos port and volume descriptions of a service.
func casaosDescriptions(extensions types.Extensions) serviceDescriptions {
	result := serviceDescriptions{ports: map[string]string{}, volumes: map[string]string{}}
	casaos, _ := extensions["x-casaos"].(map[string]any)
	for key, target := range map[string]map[string]string{"ports": result.ports, "volumes": result.volumes} {
		items, _ := casaos[key].([]any)
		for _, item := range items {
			entry, _ := item.(map[string]any)
			container := fmt.Sprint(entry["container"])
			if text := localized(entry["description"]); text != "" && container != "" {
				target[container] = text
			}
		}
	}
	return result
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

// SharesFolders reports whether the plan mounts anything from the Shared
// folder; the app identity then needs the Shared folder's ACL.
func (p Plan) SharesFolders() bool {
	for _, mount := range p.Mounts {
		if mount.Kind == MountShared {
			return true
		}
	}
	return false
}

// HostFolders lists the app-data and Shared folders the Host Agent must
// create before the install.
func (p Plan) HostFolders() []string {
	var folders []string
	for _, mount := range p.Mounts {
		if mount.Kind == MountAppData || mount.Kind == MountShared {
			folders = appendUnique(folders, mount.HostPath)
		}
	}
	return folders
}
