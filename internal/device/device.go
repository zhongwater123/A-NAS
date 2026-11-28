// Package device keeps the runtime state of the family's e-ink screens and
// decides when to refresh one (ADR 0041). The screens are family-level
// hardware that poll the Product Service over the LAN; the service alone
// decides whether a screen refreshes, fully or partially, and the decision is
// the pure function Decide below. The state and the screens' reports live in
// device.db on the system disk, beside the Product Service's other state.
package device

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/zhongwater123/A-NAS/internal/sqlitedb"
)

// The picture a screen shows. ADR 0041 fixes only the wire shape — one bit
// per pixel, most significant bit first — and leaves what the picture is for
// later. A zero bit is white, so a fresh frame is blank until the contents
// are decided.
const (
	ScreenWidth  = 800
	ScreenHeight = 480
	// ScreenBytes is 800×480 pixels at one bit each.
	ScreenBytes = ScreenWidth * ScreenHeight / 8
	// BarWidth and BarHeight are the bottom strip served at /screen-bar.bin.
	BarWidth  = 592
	BarHeight = 80
	// BarBytes is 592×80 pixels at one bit each.
	BarBytes = BarWidth * BarHeight / 8
)

// Screen is the frame /screen.bin serves and the hash Decide compares against.
func Screen() []byte { return make([]byte, ScreenBytes) }

// Bar is the bottom strip /screen-bar.bin serves. Like Screen it is blank
// until the bar's contents are decided.
func Bar() []byte { return make([]byte, BarBytes) }

// Hash is the SHA-256 of a frame, sent as X-Content-Hash and compared to tell
// whether the screen's picture changed.
func Hash(frame []byte) string {
	sum := sha256.Sum256(frame)
	return hex.EncodeToString(sum[:])
}

// Action is what the screen does next. The firmware understands none, partial
// and full only.
type Action string

const (
	ActionNone    Action = "none"
	ActionPartial Action = "partial"
	ActionFull    Action = "full"
)

// MaxPartials is how many partial refreshes a screen may run before the next
// change is a full one (ADR 0041, decision 4).
const MaxPartials = 5

// The reasons Decide returns, in the words of the ADR.
const (
	ReasonPowerOn            = "开机"
	ReasonFirstContact       = "第一次连上"
	ReasonDirtyCatchUp       = "恢复连接后补刷"
	ReasonForced             = "有人按开关"
	ReasonNight              = "夜里不刷，变化攒到早上"
	ReasonUnchanged          = "内容没变"
	ReasonFirstOfDay         = "当天第一次全刷"
	ReasonPartialsExhausted  = "局刷已满，全刷"
	ReasonPartialUnsupported = "屏不支持局刷，全刷"
	ReasonChanged            = "内容变了，局刷"
)

// NightPollSeconds is how long a screen sleeps between polls through the
// night (22:00–7:00), when it is not refreshed.
const NightPollSeconds = 30 * 60

// IsNight reports whether t is inside the night window in its own location.
func IsNight(t time.Time) bool {
	hour := t.Hour()
	return hour >= 22 || hour < 7
}

// NextPollSeconds is how long a screen sleeps before its next poll: the
// administrator's daytime interval, or the night interval after 22:00.
func NextPollSeconds(now time.Time, daytime int) int {
	if IsNight(now) {
		return NightPollSeconds
	}
	return daytime
}

// Report is what a screen told us about itself on its last poll.
type Report struct {
	Battery    int    `json:"bat"`
	Millivolts int    `json:"mv"`
	Charging   bool   `json:"chg"`
	USB        bool   `json:"usb"`
	RSSI       int    `json:"rssi"`
	AwakeMS    int64  `json:"awake_ms"`
	Firmware   string `json:"fw"`
	Wake       string `json:"wake"`
	Reset      string `json:"reset"`
	Fail       string `json:"fail"`
}

// State is one screen's runtime state (ADR 0041, decision 5): how it refreshes,
// what it reported last, and when it should next come by. A missed next_at is
// shown on the web as 该来没来, never on the screen.
type State struct {
	TokenID     string
	Partials    int
	FullDay     string
	NightHeld   bool
	ContentHash string
	Report      Report
	LastSeenAt  time.Time
	NextAt      time.Time
}

// Input is everything Decide needs to choose a screen's next action.
type Input struct {
	TokenID  string
	Shown    bool
	Dirty    bool
	Force    bool
	Partial  bool
	Hash     string
	Now      time.Time
	Location *time.Location
}

// Result is Decide's answer: the action, why, and the state to remember.
type Result struct {
	Action Action
	Reason string
	State  State
}

// Decide chooses a screen's next action, in the order ADR 0041 fixes. It is
// pure: it reads only the input and the previous state, and returns the state
// to persist. A nil state means the service has no record of the screen yet.
func Decide(in Input, state *State) Result {
	location := in.Location
	if location == nil {
		location = time.Local
	}
	now := in.Now.In(location)
	today := now.Format("2006-01-02")

	current := State{TokenID: in.TokenID}
	if state != nil {
		current = *state
	}
	current.TokenID = in.TokenID
	current.LastSeenAt = in.Now.UTC()

	full := func(reason string) Result {
		current.Partials = 0
		current.FullDay = today
		current.NightHeld = false
		current.ContentHash = in.Hash
		return Result{Action: ActionFull, Reason: reason, State: current}
	}

	switch {
	case !in.Shown:
		return full(ReasonPowerOn)
	case state == nil:
		return full(ReasonFirstContact)
	case in.Dirty:
		return full(ReasonDirtyCatchUp)
	case in.Force:
		return full(ReasonForced)
	case IsNight(now):
		if state.ContentHash != in.Hash {
			current.NightHeld = true
		}
		return Result{Action: ActionNone, Reason: ReasonNight, State: current}
	case state.ContentHash == in.Hash:
		return Result{Action: ActionNone, Reason: ReasonUnchanged, State: current}
	case state.FullDay != today:
		return full(ReasonFirstOfDay)
	case state.Partials >= MaxPartials:
		return full(ReasonPartialsExhausted)
	case !in.Partial:
		return full(ReasonPartialUnsupported)
	default:
		current.Partials++
		current.ContentHash = in.Hash
		current.NightHeld = false
		return Result{Action: ActionPartial, Reason: ReasonChanged, State: current}
	}
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sqlitedb.Open(path, sqlitedb.Options{})
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(context.Background(), db, databaseSchema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// State returns the screen's stored state, or nil when it has none.
func (s *Store) State(ctx context.Context, tokenID string) (*State, error) {
	state := State{}
	var fullDay, lastSeen, nextAt string
	var nightHeld, charging, usb int
	err := s.db.QueryRowContext(ctx, `SELECT token_id, partials, full_day, night_held, content_hash,
		battery, millivolts, charging, usb, rssi, firmware, awake_ms, wake_reason, reset_reason, last_failure,
		last_seen_at, next_at FROM device_state WHERE token_id = ?`, tokenID).Scan(
		&state.TokenID, &state.Partials, &fullDay, &nightHeld, &state.ContentHash,
		&state.Report.Battery, &state.Report.Millivolts, &charging, &usb, &state.Report.RSSI, &state.Report.Firmware,
		&state.Report.AwakeMS, &state.Report.Wake, &state.Report.Reset, &state.Report.Fail,
		&lastSeen, &nextAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	state.FullDay = fullDay
	state.NightHeld = nightHeld != 0
	state.Report.Charging = charging != 0
	state.Report.USB = usb != 0
	state.LastSeenAt = parseTime(lastSeen)
	state.NextAt = parseTime(nextAt)
	return &state, nil
}

// Save writes a screen's state, creating it when it has none.
func (s *Store) Save(ctx context.Context, state State) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_state(
		token_id, partials, full_day, night_held, content_hash,
		battery, millivolts, charging, usb, rssi, firmware, awake_ms, wake_reason, reset_reason, last_failure,
		last_seen_at, next_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(token_id) DO UPDATE SET
		partials = excluded.partials, full_day = excluded.full_day, night_held = excluded.night_held,
		content_hash = excluded.content_hash, battery = excluded.battery, millivolts = excluded.millivolts,
		charging = excluded.charging, usb = excluded.usb, rssi = excluded.rssi, firmware = excluded.firmware,
		awake_ms = excluded.awake_ms, wake_reason = excluded.wake_reason, reset_reason = excluded.reset_reason,
		last_failure = excluded.last_failure, last_seen_at = excluded.last_seen_at, next_at = excluded.next_at`,
		state.TokenID, state.Partials, state.FullDay, boolInt(state.NightHeld), state.ContentHash,
		state.Report.Battery, state.Report.Millivolts, boolInt(state.Report.Charging), boolInt(state.Report.USB),
		state.Report.RSSI, state.Report.Firmware, state.Report.AwakeMS, state.Report.Wake, state.Report.Reset,
		state.Report.Fail, formatTime(state.LastSeenAt), formatTime(state.NextAt))
	return err
}

// databaseSchema is device.db's (sqlitedb.Migrate).
var databaseSchema = sqlitedb.Schema{
	Name:     "device.db",
	Baseline: sqlitedb.Statements(baselineStatements),
}

const baselineStatements = `
CREATE TABLE IF NOT EXISTS device_state (
    token_id TEXT PRIMARY KEY,
    partials INTEGER NOT NULL DEFAULT 0,
    full_day TEXT NOT NULL DEFAULT '',
    night_held INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL DEFAULT '',
    battery INTEGER NOT NULL DEFAULT 0,
    millivolts INTEGER NOT NULL DEFAULT 0,
    charging INTEGER NOT NULL DEFAULT 0,
    usb INTEGER NOT NULL DEFAULT 0,
    rssi INTEGER NOT NULL DEFAULT 0,
    firmware TEXT NOT NULL DEFAULT '',
    awake_ms INTEGER NOT NULL DEFAULT 0,
    wake_reason TEXT NOT NULL DEFAULT '',
    reset_reason TEXT NOT NULL DEFAULT '',
    last_failure TEXT NOT NULL DEFAULT '',
    last_seen_at TEXT NOT NULL DEFAULT '',
    next_at TEXT NOT NULL DEFAULT ''
);`

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return sqlitedb.FormatTime(value)
}

func parseTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
