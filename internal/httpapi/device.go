package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/device"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/localorigin"
	"github.com/zhongwater123/A-NAS/internal/webentry"
)

// The answers of the device posture that only its guard gives.
const (
	deviceBrowserRefused = "device routes take no requests from a browser"
	deviceHostRefused    = "the request does not name this device on the LAN"
)

// guardDevice enforces the device posture (ADR 0041): a family device token
// as a bearer token, GET only, and only the three screen paths. A browser
// sends Origin and is refused; unlike the agent posture the screen is a
// LAN-only device, so any internet request is refused too, and the Host must
// name this device by localhost or an IP address (DNS rebinding). A client
// that keeps presenting bad tokens waits as one that keeps failing to sign
// in does. The Product Service is itself the final handler, so it sets no
// downstream internal headers.
func (h *productHandler) guardDevice(route apiroute.Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.Accounts == nil {
			httpjson.WriteError(w, http.StatusServiceUnavailable, "accounts_unavailable", "account service is unavailable")
			return
		}
		if r.Header.Get("Origin") != "" {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", deviceBrowserRefused)
			return
		}
		if _, internet := webentry.Internet(r); internet || !localorigin.DeviceHost(r.Host) {
			httpjson.WriteError(w, http.StatusForbidden, "forbidden", deviceHostRefused)
			return
		}
		token, ok := bearerToken(r)
		if !ok {
			refuseDevice(w, "a device token is required")
			return
		}
		caller, err := h.Accounts.ResolveDeviceToken(withClientOrigin(r), token)
		if throttled, ok := errors.AsType[*accounts.ThrottledError](err); ok {
			w.Header().Set("Retry-After", throttled.RetryAfterSeconds())
			httpjson.WriteError(w, http.StatusTooManyRequests, "too_many_attempts", "too many invalid device tokens from this client; try again later")
			return
		}
		if errors.Is(err, accounts.ErrDeviceTokenInvalid) {
			refuseDevice(w, "the device token is invalid or revoked; register the screen again")
			return
		}
		if err != nil {
			httpjson.WriteInternal(w, r, h.Logger, err)
			return
		}
		route.Handler(w, r.WithContext(accounts.WithDeviceToken(r.Context(), caller)), accounts.Session{})
	}
}

// refuseDevice answers 401 the way a bearer client recognizes.
func refuseDevice(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="A-NAS"`)
	httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", message)
}

// nextResponse is /api/device/next's answer (ADR 0041, decision 3). The
// screen fetches the frame itself: bin names /screen.bin when the action is
// not none. key is the hash of the frame the screen now shows, which
// /screen.bin repeats as X-Content-Hash. bar says the bottom strip exists;
// quiet is the night window, when the screen stays silent.
type nextResponse struct {
	Action          string `json:"action"`
	Reason          string `json:"reason"`
	NextPollSeconds int    `json:"next_poll_seconds"`
	PartialsLeft    int    `json:"partials_left"`
	Quiet           bool   `json:"quiet"`
	Key             string `json:"key"`
	ServerTime      string `json:"server_time"`
	NextAt          string `json:"next_at"`
	Bar             bool   `json:"bar"`
	Bin             string `json:"bin,omitempty"`
}

func (h *productHandler) handleDeviceNext(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	token, ok := accounts.DeviceTokenFrom(r.Context())
	if !ok {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "a device token is required")
		return
	}
	if h.Device == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "device_unavailable", "device service is unavailable")
		return
	}
	query := r.URL.Query()
	report := device.Report{
		Battery:    queryInt(query, "bat"),
		Millivolts: queryInt(query, "mv"),
		Charging:   queryBool(query, "chg"),
		USB:        queryBool(query, "usb"),
		RSSI:       queryInt(query, "rssi"),
		AwakeMS:    queryInt64(query, "awake_ms"),
		Firmware:   query.Get("fw"),
		Wake:       query.Get("wake"),
		Reset:      query.Get("reset"),
		Fail:       query.Get("fail"),
	}
	now := time.Now()
	state, err := h.Device.State(r.Context(), token.ID)
	if err != nil {
		httpjson.WriteInternal(w, r, h.Logger, err)
		return
	}
	result := device.Decide(device.Input{
		TokenID:  token.ID,
		Shown:    queryBool(query, "shown"),
		Dirty:    queryBool(query, "dirty"),
		Force:    queryBool(query, "force"),
		Partial:  queryBool(query, "partial"),
		Hash:     device.Hash(device.Screen()),
		Now:      now,
		Location: time.Local,
	}, state)
	nextPoll := device.NextPollSeconds(now, token.PollSeconds)
	result.State.Report = report
	result.State.NextAt = now.Add(time.Duration(nextPoll) * time.Second)
	if err := h.Device.Save(r.Context(), result.State); err != nil {
		httpjson.WriteInternal(w, r, h.Logger, err)
		return
	}
	httpjson.Write(w, http.StatusOK, nextResponse{
		Action:          string(result.Action),
		Reason:          result.Reason,
		NextPollSeconds: nextPoll,
		PartialsLeft:    max(0, device.MaxPartials-result.State.Partials),
		Quiet:           device.IsNight(now),
		Key:             result.State.ContentHash,
		ServerTime:      now.UTC().Format(timeFormat),
		NextAt:          result.State.NextAt.UTC().Format(timeFormat),
		Bar:             true,
		Bin:             binPath(result.Action),
	})
}

// binPath names the frame the screen fetches after a refresh decision.
func binPath(action device.Action) string {
	if action == device.ActionNone {
		return ""
	}
	return "/screen.bin"
}

// serveScreen serves the 800×480 1-bit frame, one bit per pixel, most
// significant bit first, with X-Content-Hash for the screen to compare
// (ADR 0041, decision 3).
func (h *productHandler) serveScreen(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.Device == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "device_unavailable", "device service is unavailable")
		return
	}
	frame := device.Screen()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Hash", device.Hash(frame))
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

// serveScreenBar serves the 592×80 bottom strip with X-Bar-Time. The bar's
// contents are decided later, so it is blank and its time is the current one.
func (h *productHandler) serveScreenBar(w http.ResponseWriter, r *http.Request, _ accounts.Session) {
	if h.Device == nil {
		httpjson.WriteError(w, http.StatusServiceUnavailable, "device_unavailable", "device service is unavailable")
		return
	}
	frame := device.Bar()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Bar-Time", time.Now().UTC().Format(time.RFC3339))
	w.Header().Set("Content-Length", strconv.Itoa(len(frame)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frame)
}

// queryBool reads a flag the screen sends as ?name, ?name=1 or ?name=true.
func queryBool(query url.Values, name string) bool {
	values, ok := query[name]
	if !ok {
		return false
	}
	if len(values) == 0 {
		return true
	}
	value := values[0]
	return value == "" || value == "1" || strings.EqualFold(value, "true")
}

func queryInt(query url.Values, name string) int {
	value, _ := strconv.Atoi(query.Get(name))
	return value
}

func queryInt64(query url.Values, name string) int64 {
	value, _ := strconv.ParseInt(query.Get(name), 10, 64)
	return value
}
