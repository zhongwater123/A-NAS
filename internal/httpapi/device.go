package httpapi

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// guardDevice lets an e-ink screen read its picture with its device token
// (docs/specs/eink-screens.md). A screen sends no cookie and no Origin; a
// request with an Origin header came from a browser and is refused. The
// token travels to the family service as an internal header, and the family
// service, which keeps the screens, decides whether it belongs to one. A
// client that keeps presenting tokens the family service refuses is held off
// for a while.
func (h *productHandler) guardDevice(route apiroute.Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", "e-ink screens are not called from a browser")
			return
		}
		client := clientOrigin(r)
		key := client.Via + " " + client.Address
		if wait := h.devices.held(key); wait > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
			httpjson.WriteError(w, http.StatusTooManyRequests, "too_many_attempts", "too many invalid device tokens from this client; try again later")
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "a device token is required")
			return
		}
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		ctx := serviceproxy.WithInternalHeader(r.Context(), serviceproxy.DeviceTokenHeader, token)
		route.Handler(recorder, r.WithContext(ctx), accounts.Session{})
		switch {
		case recorder.status == http.StatusUnauthorized:
			h.devices.failed(key)
		case recorder.status < http.StatusBadRequest:
			h.devices.passed(key)
		}
	}
}

// statusRecorder remembers the status a handler answered with.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// A client that presents deviceFailures refused tokens within deviceWindow is
// refused with 429 until the window, counted from its first failure, ends.
// A screen asks every 15 minutes, so a paired one never comes near it.
const (
	deviceFailures = 10
	deviceWindow   = 10 * time.Minute
)

// deviceLimiter counts refused device tokens by client. The zero value is
// ready; now defaults to time.Now.
type deviceLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]deviceFailureCount
}

type deviceFailureCount struct {
	first time.Time
	count int
}

func (l *deviceLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// held is how long the client still has to wait, or zero.
func (l *deviceLimiter) held(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	failures, ok := l.clients[key]
	if !ok {
		return 0
	}
	left := failures.first.Add(deviceWindow).Sub(l.clock())
	if left <= 0 {
		delete(l.clients, key)
		return 0
	}
	if failures.count < deviceFailures {
		return 0
	}
	return left
}

func (l *deviceLimiter) failed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.clients == nil {
		l.clients = map[string]deviceFailureCount{}
	}
	now := l.clock()
	failures, ok := l.clients[key]
	if !ok || now.Sub(failures.first) >= deviceWindow {
		failures = deviceFailureCount{first: now}
	}
	failures.count++
	l.clients[key] = failures
}

func (l *deviceLimiter) passed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.clients, key)
}
