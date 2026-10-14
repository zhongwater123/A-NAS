package appstore

import (
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/zhongwater123/A-NAS/internal/appid"
)

// How the services of an app get their Linux identity (ADR 0022). Docker
// gives a container process the user and groups of its Compose service and
// of the image's own /etc/group, never the groups of a host account, and a
// container that runs as root keeps capabilities that ignore file
// permissions and ACLs. So the Shared folder is mounted only into services
// that run as the app identity, and they reach it through the group the
// plan gives them.

// RunAs is how a service's processes get their identity.
type RunAs string

const (
	// RunAsApp starts the service as the app identity: Compose user
	// app-<id>, no capabilities.
	RunAsApp RunAs = "app"
	// RunAsAppAfterInit starts the service as root; the image's entrypoint
	// prepares it and switches to the identity in PUID/PGID, which the plan
	// sets to the app identity.
	RunAsAppAfterInit RunAs = "app-after-init"
	// RunAsImage keeps the user the image chooses, which may be root with
	// Docker's default capabilities. Such a service may only mount its app's
	// own data.
	RunAsImage RunAs = "image"
)

// SharedAccess is what a service can do in the Shared folder.
type SharedAccess string

const (
	SharedNone  SharedAccess = ""
	SharedRead  SharedAccess = "read"
	SharedWrite SharedAccess = "write"
)

// ServiceRuntime is how one service of a plan runs. The engine checks the
// containers Compose creates against it.
type ServiceRuntime struct {
	Service string
	RunAs   RunAs
	// User is the Compose user; empty when the image or its entrypoint
	// chooses the identity.
	User string
	// Groups are the supplementary groups Docker adds to the service.
	Groups []string
	// CapDrop lists the capabilities removed from the service.
	CapDrop []string
	Shared  SharedAccess
}

// RuntimeLabel marks containers with the runtime rules they were rendered
// under; containers without it, or with an older value, were installed
// before the current rules and keep running as they were until reinstalled.
const (
	RuntimeLabel  = "io.a-nas.runtime"
	RuntimePolicy = "1"
)

// noNewPrivileges keeps setuid binaries and file capabilities from raising
// a service's privileges after it starts.
const noNewPrivileges = "no-new-privileges:true"

// reviewedRunAs names services that declare neither a Compose user nor
// PUID/PGID but run as any UID, so they run as the app identity instead of
// the image's user. The evidence for each is in the catalog review
// (docs/research/app-catalog-review-2026-10.md).
var reviewedRunAs = map[string]map[string]RunAs{
	// The image user (n8n's node, UID 1000; Grafana's 472; PsiTransfer's
	// node) could not write the folders the Host Agent creates for app-<id>
	// and the container kept restarting (Demo test report, n8n).
	"grafana":     {"grafana": RunAsApp},
	"n8n":         {"n8n": RunAsApp},
	"psitransfer": {"psitransfer": RunAsApp},
	// These images start as root but mount the Shared folder, so they must
	// run as the app identity.
	"audiobookshelf": {"audiobookshelf": RunAsApp},
	"immich":         {"immich-server": RunAsApp},
	"kavita":         {"kavita": RunAsApp},
	"navidrome":      {"navidrome": RunAsApp},
	"photoprism":     {"photoprism": RunAsApp},
	"swingmusic":     {"swingmusic": RunAsApp},
}

// sharedWrites lists, by container path, the Shared folders an app writes
// because writing is what it is for: downloads, sync, backups and restores,
// uploads and library tools that edit the files. Every other Shared mount
// is read-only.
var sharedWrites = map[string][]string{
	// Uploads books, downloads podcast episodes, embeds metadata and merges
	// files into m4b in the library.
	"audiobookshelf": {"/audiobooks", "/podcasts"},
	"calibre-web":    {"/books"},
	"deluge":         {"/DATA"},
	// Restores to the original location write the backed-up folder.
	"duplicati":    {"/backups", "/source"},
	"immich":       {"/usr/src/app/upload"},
	"photoprism":   {"/photoprism/originals"},
	"pyload":       {"/downloads"},
	"qbittorrent":  {"/DATA"},
	"resilio-sync": {"/downloads"},
	"syncthing":    {"/DATA"},
	"transmission": {"/downloads", "/watch"},
}

// runAsFor decides how a service of the manifest runs.
func runAsFor(appID, name string, service types.ServiceConfig) RunAs {
	if runAs, ok := reviewedRunAs[appID][name]; ok {
		return runAs
	}
	if strings.TrimSpace(service.User) != "" {
		return RunAsApp
	}
	if declaresPUID(service) {
		return RunAsAppAfterInit
	}
	return RunAsImage
}

func declaresPUID(service types.ServiceConfig) bool {
	_, puid := service.Environment["PUID"]
	_, pgid := service.Environment["PGID"]
	return puid || pgid
}

// sharedWritable reports whether an app may write the Shared folder it
// mounts at target.
func sharedWritable(appID, target string) bool {
	return slices.Contains(sharedWrites[appID], target)
}

// applyRuntime sets the service's identity, groups and privilege limits;
// shared says whether it mounts the Shared folder. Upstream values in the
// manifest never reach the rendered service: an explicit user or literal
// PUID/PGID such as 1000 would escape the app identity.
func applyRuntime(name string, service *types.ServiceConfig, runAs RunAs, identity Identity, shared SharedAccess) ServiceRuntime {
	uid := strconv.Itoa(identity.UID)
	gid := strconv.Itoa(identity.GID)
	sharedGID := strconv.Itoa(appid.SharedGID)
	runtime := ServiceRuntime{Service: name, RunAs: runAs, Shared: shared}
	switch runAs {
	case RunAsApp:
		service.User = uid + ":" + gid
		service.CapDrop = []string{"ALL"}
		service.GroupAdd = nil
		if shared != SharedNone {
			service.GroupAdd = []string{sharedGID}
		}
		runtime.User, runtime.Groups, runtime.CapDrop = service.User, service.GroupAdd, service.CapDrop
		setEnvironment(service, "PUID", uid, false)
		setEnvironment(service, "PGID", gid, false)
	case RunAsAppAfterInit:
		// The entrypoint switches users with the groups the image's
		// /etc/group gives them, dropping any Docker added, so the Shared
		// group is the primary group the entrypoint switches to.
		group := gid
		if shared != SharedNone {
			group = sharedGID
		}
		setEnvironment(service, "PUID", uid, true)
		setEnvironment(service, "PGID", group, true)
		runtime.CapDrop = service.CapDrop
	case RunAsImage:
		runtime.User, runtime.CapDrop = service.User, service.CapDrop
	}
	service.SecurityOpt = []string{noNewPrivileges}
	return runtime
}

// setEnvironment sets a variable the service declares, or always when force.
func setEnvironment(service *types.ServiceConfig, name, value string, force bool) {
	if _, declared := service.Environment[name]; !declared && !force {
		return
	}
	if service.Environment == nil {
		service.Environment = types.MappingWithEquals{}
	}
	service.Environment[name] = &value
}
