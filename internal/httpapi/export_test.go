package httpapi

import (
	"net/http"

	"github.com/zhongwater123/A-NAS/internal/apiroute"
)

// RoutePattern is the pattern of the product route that would serve r; "/"
// is the catch-all that answers not_found.
func RoutePattern(product http.Handler, r *http.Request) string {
	_, pattern := product.(*productHandler).mux.Handler(r)
	return pattern
}

// Routes is the product's route table.
func Routes(product http.Handler) []apiroute.Route {
	return product.(*productHandler).table()
}
