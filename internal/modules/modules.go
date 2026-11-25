// Package modules plugs removable modules into A-NAS (ADR 0037). A module is
// a feature with its own program, account, units and store that its own
// scripts install and remove; A-NAS only forwards its API, confirms sessions
// for it and keeps its store. Nothing here names a particular module: the
// Product Service's registry lists which modules it knows, and the Host
// Agent learns which are installed from their manifests (modulemanifest).
package modules

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/modulemanifest"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// PathPrefix is where the Product Service serves every module's API:
// /api/v1/modules/<id>/….
const PathPrefix = "/api/v1/modules"

// Mount is one prefix of a module's API with the posture the Product
// Service enforces before forwarding; the module authorizes again itself.
type Mount struct {
	Prefix    string
	Posture   apiroute.Posture
	Endpoints []apiroute.Endpoint
}

// Module is a registry entry of the Product Service: the module's mounts,
// the handler that serves all of them and whether it is installed now.
type Module struct {
	ID        string
	Mounts    []Mount
	Handler   http.Handler
	Installed func() bool
}

// Proxy forwards a module's API to its socket in socketDir with only the
// session token. While the socket does not exist the module is not
// installed and requests answer 404 module_not_installed; while it exists
// but nothing answers, 503 module_unavailable.
func Proxy(id, socketDir string, logger *slog.Logger) (http.Handler, func() bool) {
	socket := modulemanifest.SocketPath(socketDir, id)
	installed := func() bool {
		info, err := os.Lstat(socket)
		return err == nil && info.Mode()&os.ModeSocket != 0
	}
	forward := serviceproxy.New(socket, serviceproxy.Unavailable{
		Code: "module_unavailable", Message: "the module is installed but not running",
	}, logger)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !installed() {
			httpjson.WriteError(w, http.StatusNotFound, "module_not_installed", "this module is not installed on the device")
			return
		}
		forward.ServeHTTP(w, r)
	}), installed
}

// Installed is the body of GET /api/v1/modules.
type Installed struct {
	Items []InstalledModule `json:"items"`
}

type InstalledModule struct {
	ID string `json:"id"`
}

// List answers which registered modules are installed now.
func List(registry []Module) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := Installed{Items: []InstalledModule{}}
		for _, module := range registry {
			if module.Installed != nil && module.Installed() {
				body.Items = append(body.Items, InstalledModule{ID: module.ID})
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		httpjson.Write(w, http.StatusOK, body)
	})
}
