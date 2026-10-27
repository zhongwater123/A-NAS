package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/activity"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/photosapi"
)

// photoActivitySource reads the caller's photo events through the photo API,
// so it works whether the library runs in this process or in the photo
// service; without a photo API the activity log leaves photos out.
func (h *productHandler) photoActivitySource() func(*http.Request, activity.Query) ([]activity.Event, error) {
	if h.photos == nil {
		return nil
	}
	return h.photoActivity
}

func (h *productHandler) photoActivity(r *http.Request, q activity.Query) ([]activity.Event, error) {
	request := r.Clone(r.Context())
	request.Method, request.Body, request.RequestURI = http.MethodGet, http.NoBody, ""
	request.URL = &url.URL{Path: photosapi.ActivityPath, RawQuery: q.Values().Encode()}
	response := &bufferedResponse{header: http.Header{}}
	h.guard(apiroute.Route{Posture: apiroute.Session, Handler: h.photoHandler()})(response, request)
	if response.status != http.StatusOK {
		return nil, fmt.Errorf("photo activity status %d", response.status)
	}
	return photosapi.DecodeActivity(&response.body)
}

// bufferedResponse keeps a response in memory for a request served in-process.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedResponse) Write(data []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(data)
}

// withClientOrigin records where a sign-in or sign-out came from.
func withClientOrigin(r *http.Request) context.Context {
	return accounts.WithClientOrigin(r.Context(), clientOrigin(r))
}

// clientOrigin is where a request came from, which sessions are also bound
// to. The LAN entry (Caddy, ADR 0012) is the only proxy and sets
// X-Forwarded-For to the client's address; the Product Service listens on
// loopback only, so anything else connected over loopback directly.
func clientOrigin(r *http.Request) accounts.ClientOrigin {
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" && r.Header.Get("Forwarded") == "" {
		return accounts.ClientOrigin{Via: accounts.ViaLoopback}
	}
	origin := accounts.ClientOrigin{Via: accounts.ViaLAN}
	hops := strings.Split(forwarded, ",")
	address := strings.TrimSpace(hops[len(hops)-1])
	// Caddy writes a link-local client's address with its zone.
	if _, err := netip.ParseAddr(address); err == nil {
		origin.Address = address
	}
	return origin
}
