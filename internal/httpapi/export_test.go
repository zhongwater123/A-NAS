package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/hoststate"
)

// New serves the host state routes alone, without asking for a session, as
// the product does once a request passed its guard.
func New(reader hoststate.Observer, dataSource DataSource, productVersion string, logger *slog.Logger) http.Handler {
	return newStateHandler(reader, dataSource, productVersion, logger)
}

// RoutePattern is the pattern of the product route that would serve r; "/"
// is the catch-all that answers not_found.
func RoutePattern(product http.Handler, r *http.Request) string {
	_, pattern := product.(*productHandler).mux.Handler(r)
	return pattern
}

// Routes is the product's route table with every mounted API expanded into
// the operations it serves.
func Routes(product http.Handler) []apiroute.Route {
	return apiroute.Expand(product.(*productHandler).table())
}
