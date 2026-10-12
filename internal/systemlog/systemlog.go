// Package systemlog describes the A-NAS service logs an administrator may
// read in the Web desktop: the allowlist of services (ADR 0020), the query a
// client sends and the lines it gets back. The Host Agent reads them from the
// systemd journal (package journal); development uses Fake.
package systemlog

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Level string

const (
	LevelError   Level = "error"
	LevelWarning Level = "warning"
	LevelInfo    Level = "info"
	LevelDebug   Level = "debug"
)

var levelRank = map[Level]int{LevelError: 0, LevelWarning: 1, LevelInfo: 2, LevelDebug: 3}

// Service is one entry of the allowlist: a key clients use and the systemd
// units, possibly templates, whose lines it covers.
type Service struct {
	Key   string
	Units []string
}

// Services is the allowlist. Only these units are ever read; a new A-NAS
// service must be added here and to the logs spec to appear in the Web.
var Services = []Service{
	{Key: "api", Units: []string{"anas-api.service"}},
	{Key: "host-agent", Units: []string{"anas-host-agent.service"}},
	{Key: "photos", Units: []string{"anas-photos.service"}},
	{Key: "containers", Units: []string{"anas-container-agent.service"}},
	{Key: "ai", Units: []string{"anas-ai.service", "anas-ai.socket"}},
	{Key: "media", Units: []string{"anas-media@*.service", "anas-media.socket"}},
	{Key: "kiosk", Units: []string{"anas-kiosk@*.service"}},
	{Key: "samba", Units: []string{"smbd.service"}},
	{Key: "caddy", Units: []string{"caddy.service"}},
}

// ServiceOfUnit returns the key of the service a unit belongs to.
func ServiceOfUnit(unit string) (string, bool) {
	for _, service := range Services {
		for _, pattern := range service.Units {
			if matched, _ := path.Match(pattern, unit); matched {
				return service.Key, true
			}
		}
	}
	return "", false
}

func knownService(key string) bool {
	for _, service := range Services {
		if service.Key == key {
			return true
		}
	}
	return false
}

// Units returns the units of the given services, or of all services.
func Units(keys []string) []string {
	var units []string
	for _, service := range Services {
		if len(keys) == 0 || contains(keys, service.Key) {
			units = append(units, service.Units...)
		}
	}
	return units
}

func contains(items []string, item string) bool {
	for _, candidate := range items {
		if candidate == item {
			return true
		}
	}
	return false
}

// Limits on what one read may ask for and return.
const (
	DefaultLimit = 200
	MaxLimit     = 500
	MaxTextRunes = 200
	// ScanLimit bounds the lines one read checks against the filters.
	ScanLimit = 20000
	// MaxMessageBytes is where a message is cut.
	MaxMessageBytes = 16 << 10
	maxFields       = 40
	maxFieldBytes   = 2 << 10
)

var ErrInvalidQuery = errors.New("system log query is invalid")

// Query selects log lines, newest first. Before continues with older lines
// than a cursor; After returns only newer ones.
type Query struct {
	Services []string
	MinLevel Level
	Since    time.Time
	Until    time.Time
	Text     string
	Before   string
	After    string
	Limit    int
}

// journald cursors are semicolon-separated key=hex pairs.
var cursorPattern = regexp.MustCompile(`^[a-z]=[0-9a-f]{1,64}(;[a-z]=[0-9a-f]{1,64}){0,9}$`)

// ParseQuery reads the filters a client sent: service, level, since, until
// (RFC 3339), q, before, after and limit.
func ParseQuery(values url.Values) (Query, error) {
	q := Query{Limit: DefaultLimit, Text: strings.TrimSpace(values.Get("q")), Before: values.Get("before"), After: values.Get("after")}
	if utf8.RuneCountInString(q.Text) > MaxTextRunes {
		return Query{}, ErrInvalidQuery
	}
	if service := values.Get("service"); service != "" {
		if !knownService(service) {
			return Query{}, ErrInvalidQuery
		}
		q.Services = []string{service}
	}
	if level := Level(values.Get("level")); level != "" {
		if _, ok := levelRank[level]; !ok {
			return Query{}, ErrInvalidQuery
		}
		q.MinLevel = level
	}
	for name, target := range map[string]*time.Time{"since": &q.Since, "until": &q.Until} {
		if raw := values.Get(name); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return Query{}, ErrInvalidQuery
			}
			*target = parsed
		}
	}
	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > MaxLimit {
			return Query{}, ErrInvalidQuery
		}
		q.Limit = limit
	}
	if (q.Before != "" && !cursorPattern.MatchString(q.Before)) || (q.After != "" && !cursorPattern.MatchString(q.After)) || (q.Before != "" && q.After != "") {
		return Query{}, ErrInvalidQuery
	}
	return q, nil
}

// Values encodes q for ParseQuery.
func (q Query) Values() url.Values {
	values := url.Values{}
	values.Set("limit", strconv.Itoa(q.Limit))
	if len(q.Services) == 1 {
		values.Set("service", q.Services[0])
	}
	if q.MinLevel != "" {
		values.Set("level", string(q.MinLevel))
	}
	if !q.Since.IsZero() {
		values.Set("since", q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		values.Set("until", q.Until.UTC().Format(time.RFC3339))
	}
	for name, value := range map[string]string{"q": q.Text, "before": q.Before, "after": q.After} {
		if value != "" {
			values.Set(name, value)
		}
	}
	return values
}

// Matches applies the filters journald cannot: level, taken from the line
// itself for JSON logs, and plain-text search.
func (q Query) Matches(entry Entry) bool {
	if q.MinLevel != "" && levelRank[entry.Level] > levelRank[q.MinLevel] {
		return false
	}
	if q.Text == "" {
		return true
	}
	text := strings.ToLower(q.Text)
	if strings.Contains(strings.ToLower(entry.Message), text) {
		return true
	}
	for key, value := range entry.Fields {
		if strings.Contains(strings.ToLower(key+"="+value), text) {
			return true
		}
	}
	return false
}

// Entry is one log line.
type Entry struct {
	Cursor    string            `json:"cursor"`
	Time      time.Time         `json:"time"`
	Service   string            `json:"service"`
	Unit      string            `json:"unit,omitempty"`
	Level     Level             `json:"level"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	PID       int               `json:"pid,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

// Page is the result of one read, newest first.
type Page struct {
	Items []Entry `json:"items"`
	// Next continues with older lines; it is empty at the start of the log.
	Next string `json:"next,omitempty"`
	// ScannedThrough is set when the read stopped at ScanLimit before the
	// page was full: lines older than it were not checked.
	ScannedThrough *time.Time `json:"scannedThrough,omitempty"`
	// Overflow is set when an After read found more lines than Limit.
	Overflow bool `json:"overflow,omitempty"`
}

// Reader reads service logs.
type Reader interface {
	ReadLogs(context.Context, Query) (Page, error)
}

// ParseMessage reads a line's level, text and fields. A-NAS services and
// Caddy write JSON objects with level and msg; other lines keep journald's
// priority (0–7) and their text.
func ParseMessage(message string, priority int) (Level, string, map[string]string) {
	level := priorityLevel(priority)
	trimmed := strings.TrimSpace(message)
	if !strings.HasPrefix(trimmed, "{") {
		return level, message, nil
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &document); err != nil {
		return level, message, nil
	}
	text, ok := stringField(document, "msg")
	if !ok {
		if text, ok = stringField(document, "message"); !ok {
			return level, message, nil
		}
	}
	if raw, ok := stringField(document, "level"); ok {
		if parsed, ok := namedLevel(raw); ok {
			level = parsed
		}
	}
	fields := map[string]string{}
	for key, value := range document {
		switch key {
		case "time", "ts", "level", "msg", "message":
			continue
		}
		if len(fields) == maxFields {
			break
		}
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			text = string(value)
		}
		fields[key] = truncate(text, maxFieldBytes)
	}
	if len(fields) == 0 {
		fields = nil
	}
	return level, text, fields
}

func stringField(document map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := document[key]
	if !ok {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// namedLevel understands slog ("ERROR", "WARN+2") and zap ("warn", "fatal").
func namedLevel(name string) (Level, bool) {
	name = strings.ToLower(name)
	if index := strings.IndexAny(name, "+-"); index > 0 {
		name = name[:index]
	}
	switch name {
	case "error", "err", "fatal", "panic", "dpanic", "crit", "critical", "alert", "emerg":
		return LevelError, true
	case "warn", "warning":
		return LevelWarning, true
	case "info", "notice":
		return LevelInfo, true
	case "debug", "trace":
		return LevelDebug, true
	}
	return "", false
}

func priorityLevel(priority int) Level {
	switch {
	case priority <= 3:
		return LevelError
	case priority == 4:
		return LevelWarning
	case priority == 7:
		return LevelDebug
	}
	return LevelInfo
}

// TruncateMessage cuts a message at MaxMessageBytes on a character boundary.
func TruncateMessage(message string) (string, bool) {
	cut := truncate(message, MaxMessageBytes)
	return cut, len(cut) < len(message)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
