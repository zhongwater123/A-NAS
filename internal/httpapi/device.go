package httpapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// guardDevice lets an e-ink screen read its picture with its device token
// (docs/specs/eink-screens.md). A screen sends no cookie and no Origin; a
// request with an Origin header came from a browser and is refused. The
// token travels to the family service as an internal header, and the family
// service, which keeps the screens, decides whether it belongs to one.
func guardDevice(route apiroute.Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "e-ink screens are not called from a browser")
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "a device token is required")
			return
		}
		ctx := serviceproxy.WithInternalHeader(r.Context(), serviceproxy.DeviceTokenHeader, token)
		route.Handler(w, r.WithContext(ctx), accounts.Session{})
	}
}
