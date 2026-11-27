package familyapi

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/apiroute"
	"github.com/zhongwater123/A-NAS/internal/eink"
	"github.com/zhongwater123/A-NAS/internal/family"
	"github.com/zhongwater123/A-NAS/internal/httpjson"
	"github.com/zhongwater123/A-NAS/internal/serviceproxy"
)

// E-ink screens (docs/specs/eink-screens.md). Administrators pair them under
// ScreensPrefix; the screens ask under EinkPrefix with their device tokens,
// which arrive as serviceproxy.DeviceTokenHeader.
const (
	EinkPrefix    = "/api/v1/eink"
	ScreensPrefix = EinkPrefix + "/screens"
	// The three device paths the firmware knows (0.2.5 and later).
	NextPath    = EinkPrefix + "/next"
	PicturePath = EinkPrefix + "/screen.bin"
	BarPath     = EinkPrefix + "/screen-bar.bin"
)

// deviceTime is how the firmware reads server_time and next_at: local time
// to the second, without a zone.
const deviceTime = "2006-01-02T15:04:05"

func (h *handler) screenRoutes() map[string]route {
	return map[string]route{
		"GET ":               h.screens,
		"POST ":              h.addScreen,
		"PATCH /{screenID}":  h.renameScreen,
		"DELETE /{screenID}": h.removeScreen,
	}
}

func (h *handler) deviceRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET " + NextPath:    h.deviceNext,
		"GET " + PicturePath: h.devicePicture,
		"GET " + BarPath:     h.deviceBar,
	}
}

// ScreenEndpoints are the operations administrators use under ScreensPrefix.
func ScreenEndpoints() []apiroute.Endpoint { return endpointsUnder(ScreensPrefix) }

// DeviceEndpoints are the three operations screens call with device tokens.
func DeviceEndpoints() []apiroute.Endpoint {
	var endpoints []apiroute.Endpoint
	for _, endpoint := range endpointsUnder(EinkPrefix) {
		if !strings.HasPrefix(endpoint.Pattern, ScreensPrefix) {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

func endpointsUnder(prefix string) []apiroute.Endpoint {
	var endpoints []apiroute.Endpoint
	for _, endpoint := range New(nil, nil, nil).(*handler).mux.Endpoints() {
		if endpoint.Pattern == prefix || strings.HasPrefix(endpoint.Pattern, prefix+"/") {
			endpoints = append(endpoints, endpoint)
		}
	}
	return endpoints
}

// administrator refuses anyone but an administrator, besides the Product
// Service's own check of the admin posture.
func administrator(w http.ResponseWriter, r *http.Request) bool {
	user, _ := accounts.UserFrom(r.Context())
	if user.Role != accounts.RoleAdmin {
		httpjson.WriteError(w, http.StatusForbidden, "forbidden", "administrator permission is required")
		return false
	}
	return true
}

type screenName struct {
	Name string `json:"name"`
}

type pairedScreen struct {
	Screen family.Screen `json:"screen"`
	// Token is shown this once; the NAS keeps only its hash.
	Token string `json:"token"`
}

func (h *handler) screens(w http.ResponseWriter, r *http.Request, _ family.Actor) {
	if !administrator(w, r) {
		return
	}
	screens, err := h.store.Screens(r.Context())
	h.reply(w, r, http.StatusOK, items[family.Screen]{Items: screens}, err)
}

func (h *handler) addScreen(w http.ResponseWriter, r *http.Request, actor family.Actor) {
	if !administrator(w, r) {
		return
	}
	var body screenName
	if !httpjson.Read(w, r, 1<<10, &body) {
		return
	}
	screen, token, err := h.store.AddScreen(r.Context(), actor, body.Name)
	h.reply(w, r, http.StatusCreated, pairedScreen{Screen: screen, Token: token}, err)
}

func (h *handler) renameScreen(w http.ResponseWriter, r *http.Request, _ family.Actor) {
	if !administrator(w, r) {
		return
	}
	var body screenName
	if !httpjson.Read(w, r, 1<<10, &body) {
		return
	}
	screen, err := h.store.RenameScreen(r.Context(), r.PathValue("screenID"), body.Name)
	h.reply(w, r, http.StatusOK, screen, err)
}

func (h *handler) removeScreen(w http.ResponseWriter, r *http.Request, _ family.Actor) {
	if !administrator(w, r) {
		return
	}
	h.reply(w, r, http.StatusNoContent, nil, h.store.RemoveScreen(r.Context(), r.PathValue("screenID")))
}

// device finds the screen behind the request's device token.
func (h *handler) device(w http.ResponseWriter, r *http.Request) (family.Screen, bool) {
	screen, err := h.store.ScreenByToken(r.Context(), r.Header.Get(serviceproxy.DeviceTokenHeader))
	if errors.Is(err, family.ErrScreenToken) {
		httpjson.WriteError(w, http.StatusUnauthorized, "authentication_required", "the device token is invalid or the screen was removed")
		return family.Screen{}, false
	}
	if err != nil {
		h.fail(w, r, err)
		return family.Screen{}, false
	}
	return screen, true
}

// deviceAnswer is what a screen is told; the firmware reads these names.
type deviceAnswer struct {
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	Key        string `json:"key"`
	Bin        string `json:"bin,omitempty"`
	Bar        string `json:"bar"`
	ServerTime string `json:"server_time"`
	NextAt     string `json:"next_at"`
}

func (h *handler) deviceNext(w http.ResponseWriter, r *http.Request) {
	screen, ok := h.device(w, r)
	if !ok {
		return
	}
	todos, err := h.store.ScreenTodos(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	now := h.store.Now()
	board := family.TodoBoard(todos, h.store.Today())
	key := board.Key() + ":bw"
	query := r.URL.Query()
	shown := query.Get("shown")
	decision := eink.Decide(eink.Ask{
		Now: now, Known: screen.SeenAt != nil, Shown: shown, Current: key,
		Dirty: query.Get("dirty") == "1", Pressed: query.Get("force") == "手动",
	})
	answer := deviceAnswer{Action: decision.Action, Reason: decision.Reason, Key: key, Bar: BarPath,
		ServerTime: now.Format(deviceTime), NextAt: decision.NextAt.Format(deviceTime)}
	if decision.Action == eink.ActionFull {
		answer.Bin = PicturePath + "?h=" + board.Key()
		shown = key
	}
	if err := h.store.RecordAsk(r.Context(), screen.ID, shown, now, decision.NextAt, deviceReport(query)); err != nil {
		h.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, answer)
}

// fonts are read once, on the first picture: Noto Sans CJK from Debian's
// fonts-noto-cjk, which the a-nas package depends on.
var fonts = sync.OnceValues(func() (*eink.Fonts, error) { return eink.LoadFonts(eink.FontDir) })

// drawer finds the fonts or tells the screen pictures are unavailable, in
// which case it keeps what it shows and asks again later.
func (h *handler) drawer(w http.ResponseWriter, r *http.Request) (*eink.Fonts, bool) {
	f, err := fonts()
	if err != nil {
		h.logger.ErrorContext(r.Context(), "e-ink screens cannot draw", "error", err)
		httpjson.WriteError(w, http.StatusServiceUnavailable, "eink_unavailable", "pictures for e-ink screens cannot be drawn")
		return nil, false
	}
	return f, true
}

// devicePicture draws the board as it is now; its key, which the screen
// keeps as what it shows, travels in X-Content-Hash.
func (h *handler) devicePicture(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.device(w, r); !ok {
		return
	}
	f, ok := h.drawer(w, r)
	if !ok {
		return
	}
	todos, err := h.store.ScreenTodos(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	board := family.TodoBoard(todos, h.store.Today())
	rows := make([]eink.Row, len(board.Rows))
	for i, row := range board.Rows {
		rows[i] = eink.Row{Text: row.Text, When: row.When, Urgent: row.Urgent}
	}
	picture := eink.Pack(f.Board(rows, board.More, family.BoardEmpty, board.Pet, h.store.Now()))
	w.Header().Set("X-Content-Hash", board.Key())
	writePicture(w, picture)
}

// deviceBar draws the offline bar with the time of this question, which the
// screen keeps for when it cannot reach the NAS.
func (h *handler) deviceBar(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.device(w, r); !ok {
		return
	}
	f, ok := h.drawer(w, r)
	if !ok {
		return
	}
	writePicture(w, eink.Pack(f.Bar(h.store.Now())))
}

func writePicture(w http.ResponseWriter, picture []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(picture)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(picture)
}

var (
	reportWord = regexp.MustCompile(`^[a-z]{1,12}$`)
	reportFail = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,23}$`)
	reportFW   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,31}$`)
	reportHist = regexp.MustCompile(`^[A-Za-z0-9.-]{1,127}$`)
)

// deviceReport reads what a screen reports about itself. A malformed field
// is left out rather than failing the question (the spec's “上报字段”).
func deviceReport(query url.Values) family.ScreenReport {
	number := func(name string, low, high int64) (int64, bool) {
		value, err := strconv.ParseInt(query.Get(name), 10, 64)
		return value, err == nil && value >= low && value <= high
	}
	small := func(name string, low, high int) *int {
		if value, ok := number(name, int64(low), int64(high)); ok {
			v := int(value)
			return &v
		}
		return nil
	}
	flag := func(name string) *bool {
		switch query.Get(name) {
		case "0":
			return new(false)
		case "1":
			return new(true)
		}
		return nil
	}
	text := func(name string, pattern *regexp.Regexp) string {
		if value := query.Get(name); pattern.MatchString(value) {
			return value
		}
		return ""
	}
	report := family.ScreenReport{
		Battery: small("bat", 0, 100), Millivolt: small("mv", 0, 6000), RSSI: small("rssi", -127, 0),
		Charging: flag("chg"), USB: flag("usb"),
		Firmware: text("fw", reportFW), Wake: text("wake", reportWord), Reset: text("reset", reportWord),
		Fail: text("fail", reportFail), History: text("hist", reportHist),
	}
	if value, ok := number("awake_ms", 0, 1<<32-1); ok {
		report.AwakeMs = &value
	}
	return report
}
