package systemlog

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Fake serves invented but realistic service logs for development: a boot
// two days ago, routine work since, a failed and restarted photo service, and
// one line every 30 seconds so following the log shows something.
type Fake struct {
	start   time.Time
	entries []Entry
	now     func() time.Time
}

const fakeHeartbeat = 30 * time.Second

// NewFake builds the log as of now; lines keep arriving as time passes.
func NewFake(now func() time.Time) *Fake {
	if now == nil {
		now = time.Now
	}
	start := now().Truncate(time.Second)
	f := &Fake{start: start, now: now}
	boot := start.Add(-50 * time.Hour)
	at := func(offset time.Duration) time.Time { return boot.Add(offset) }
	systemd := func(offset time.Duration, unit string, priority int, message string) {
		f.add(at(offset), unit, priority, message, 1)
	}
	json := func(offset time.Duration, unit string, pid int, level, message string, fields ...any) {
		f.add(at(offset), unit, 6, slogLine(at(offset), level, message, fields...), pid)
	}
	systemd(2*time.Second, "anas-host-agent.service", 6, "Started anas-host-agent.service - A-NAS privileged Host Agent.")
	json(2500*time.Millisecond, "anas-host-agent.service", 612, "INFO", "file broker identity switch verified")
	json(2600*time.Millisecond, "anas-host-agent.service", 612, "INFO", "A-NAS Host Agent listening", "socket", "/run/a-nas/host-agent.sock", "file_broker", "/run/a-nas/file-broker.sock", "version", "v1.0.1-86d675d")
	json(2700*time.Millisecond, "anas-host-agent.service", 612, "INFO", "photo session lookup listening", "socket", "/run/a-nas-sessions/photos.sock")
	systemd(3*time.Second, "smbd.service", 6, "Starting smbd.service - Samba SMB Daemon...")
	f.add(at(3200*time.Millisecond), "smbd.service", 5, "smbd version 4.22.4-Debian-4.22.4+dfsg-1~deb13u1 started.", 640)
	f.add(at(3210*time.Millisecond), "smbd.service", 5, "Copyright Andrew Tridgell and the Samba Team 1992-2025", 640)
	systemd(3400*time.Millisecond, "smbd.service", 6, "Started smbd.service - Samba SMB Daemon.")
	systemd(4*time.Second, "anas-container-agent.service", 6, "Started anas-container-agent.service - A-NAS container agent.")
	json(4300*time.Millisecond, "anas-container-agent.service", 655, "INFO", "A-NAS container agent listening", "socket", "/run/a-nas-container/agent.sock")
	systemd(5*time.Second, "anas-api.service", 6, "Started anas-api.service - A-NAS product API.")
	json(5400*time.Millisecond, "anas-api.service", 671, "INFO", "A-NAS API listening", "address", "127.0.0.1:8080", "version", "v1.0.1-86d675d", "data_source", "live", "terminal", false, "containers", true)
	systemd(5*time.Second+500*time.Millisecond, "anas-photos.service", 6, "Started anas-photos.service - A-NAS photo service.")
	json(6*time.Second, "anas-photos.service", 689, "INFO", "photo store reconciled", "objects", 185, "elapsed", "412ms")
	systemd(6500*time.Millisecond, "caddy.service", 6, "Started caddy.service - Caddy.")
	f.add(at(6600*time.Millisecond), "caddy.service", 6, caddyLine(at(6600*time.Millisecond), "info", "", "using config from file", "file", "/etc/caddy/Caddyfile"), 702)
	f.add(at(6700*time.Millisecond), "caddy.service", 6, caddyLine(at(6700*time.Millisecond), "warn", "http.auto_https", "automatic HTTPS is completely disabled for server", "server_name", "srv0"), 702)
	f.add(at(6800*time.Millisecond), "caddy.service", 6, caddyLine(at(6800*time.Millisecond), "info", "", "serving initial configuration"), 702)
	systemd(9*time.Second, "anas-kiosk@tty1.service", 6, "Started anas-kiosk@tty1.service - A-NAS local console on tty1.")
	f.add(at(12*time.Second), "anas-kiosk@tty1.service", 4, "[731:731:1012/080012.441:WARNING:gpu_init.cc(441)] Hardware acceleration is unavailable.", 731)
	systemd(14*time.Minute, "anas-ai.socket", 6, "Listening on anas-ai.socket - A-NAS photo AI worker socket.")
	systemd(14*time.Minute+time.Second, "anas-ai.service", 6, "Started anas-ai.service - A-NAS photo AI worker.")
	f.add(at(14*time.Minute+3*time.Second), "anas-ai.service", 6, "model ready: chinese-clip-vit-b-16 (512 px, 70 tokens) in 2.8 s", 1204)
	systemd(31*time.Minute, "anas-ai.service", 6, "anas-ai.service: Deactivated successfully.")
	systemd(31*time.Minute+time.Second, "anas-ai.service", 6, "anas-ai.service: Consumed 16min 41.203s CPU time, 815.2M memory peak.")

	// The photo service fails once and systemd restarts it.
	json(9*time.Hour+12*time.Minute, "anas-photos.service", 689, "ERROR", "photo reconciliation failed", "error", "open /srv/a-nas/data/photos/catalog.db: read-only file system")
	systemd(9*time.Hour+12*time.Minute+time.Second, "anas-photos.service", 3, "anas-photos.service: Main process exited, code=exited, status=1/FAILURE")
	systemd(9*time.Hour+12*time.Minute+time.Second, "anas-photos.service", 4, "anas-photos.service: Failed with result 'exit-code'.")
	json(9*time.Hour+12*time.Minute+1500*time.Millisecond, "anas-api.service", 671, "WARN", "photo service unreachable", "error", "dial unix /run/a-nas-photos/photos.sock: connect: connection refused")
	systemd(9*time.Hour+12*time.Minute+3*time.Second, "anas-photos.service", 6, "anas-photos.service: Scheduled restart job, restart counter is at 1.")
	systemd(9*time.Hour+12*time.Minute+3*time.Second, "anas-photos.service", 6, "Started anas-photos.service - A-NAS photo service.")
	json(9*time.Hour+12*time.Minute+4*time.Second, "anas-photos.service", 2210, "INFO", "photo store reconciled", "objects", 185, "elapsed", "388ms")

	json(20*time.Hour, "anas-host-agent.service", 612, "WARN", "repaired drifted data-volume permissions", "paths", 2)
	json(26*time.Hour, "anas-container-agent.service", 655, "INFO", "container action requested", "container", "jellyfin", "action", "restart")
	json(26*time.Hour+2*time.Second, "anas-container-agent.service", 655, "INFO", "container action applied", "container", "jellyfin", "action", "restart")
	systemd(30*time.Hour, "anas-media.socket", 6, "Listening on anas-media.socket - A-NAS media worker socket.")
	systemd(30*time.Hour+time.Second, "anas-media@3-1418-0.service", 6, "Started anas-media@3-1418-0.service - A-NAS media worker (3-1418-0).")
	f.add(at(30*time.Hour+2*time.Second), "anas-media@3-1418-0.service", 6, "transcode started: h264 1920x1080 → 480p, audio ac3 → aac", 3302)
	json(30*time.Hour+40*time.Minute, "anas-media@3-1418-0.service", 3302, "ERROR", "A-NAS media worker failed", "error", "ffmpeg: Invalid data found when processing input")
	systemd(30*time.Hour+40*time.Minute+time.Second, "anas-media@3-1418-0.service", 6, "anas-media@3-1418-0.service: Deactivated successfully.")
	json(44*time.Hour, "anas-api.service", 671, "INFO", "music library scanned", "user_id", "user:3f2a", "tracks", 214, "read", 12, "removed", 0, "elapsed", "1.9s")
	json(47*time.Hour, "anas-host-agent.service", 612, "INFO", "viewing grant expired and revoked", "grant", "viewing:91c2")
	json(49*time.Hour+30*time.Minute, "anas-api.service", 671, "WARN", "host metrics read failed", "error", "read /proc/stat: interrupted system call")
	f.add(at(49*time.Hour+50*time.Minute), "smbd.service", 4, "check_ntlm_password: Authentication for user [alice] -> [alice] FAILED with error NT_STATUS_WRONG_PASSWORD", 4120)
	return f
}

func (f *Fake) add(at time.Time, unit string, priority int, message string, pid int) {
	service, _ := ServiceOfUnit(unit)
	level, text, fields := ParseMessage(message, priority)
	f.entries = append(f.entries, Entry{Time: at.UTC(), Service: service, Unit: unit, Level: level, Message: text, Fields: fields, PID: pid})
}

// ReadLogs answers q over the invented log, including the lines that would
// have arrived since NewFake.
func (f *Fake) ReadLogs(_ context.Context, q Query) (Page, error) {
	all := append([]Entry(nil), f.entries...)
	for at := f.start.Add(fakeHeartbeat); !at.After(f.now()); at = at.Add(fakeHeartbeat) {
		message := slogLine(at, "INFO", "media library rescanned", "library", "library:movies", "added", 0, "removed", 0)
		level, text, fields := ParseMessage(message, 6)
		all = append(all, Entry{Time: at.UTC(), Service: "api", Unit: "anas-api.service", Level: level, Message: text, Fields: fields, PID: 671})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	for index := range all {
		all[index].Cursor = "s=0;i=" + strconv.FormatInt(int64(index+1), 16)
	}
	position := func(cursor string) int {
		index, _ := strconv.ParseInt(strings.TrimPrefix(cursor, "s=0;i="), 16, 64)
		return int(index) - 1
	}
	page := Page{Items: []Entry{}}
	keep := func(entry Entry) bool {
		if len(q.Services) > 0 && !contains(q.Services, entry.Service) {
			return false
		}
		if (!q.Since.IsZero() && entry.Time.Before(q.Since)) || (!q.Until.IsZero() && !entry.Time.Before(q.Until)) {
			return false
		}
		return q.Matches(entry)
	}
	if q.After != "" {
		for index := position(q.After) + 1; index < len(all); index++ {
			if keep(all[index]) {
				if len(page.Items) == q.Limit {
					page.Overflow = true
					break
				}
				page.Items = append([]Entry{all[index]}, page.Items...)
			}
		}
		return page, nil
	}
	from := len(all) - 1
	if q.Before != "" {
		from = position(q.Before) - 1
	}
	for index := from; index >= 0; index-- {
		if keep(all[index]) {
			page.Items = append(page.Items, all[index])
			if len(page.Items) == q.Limit {
				if index > 0 {
					page.Next = all[index].Cursor
				}
				break
			}
		}
	}
	return page, nil
}

func slogLine(at time.Time, level, message string, fields ...any) string {
	document := map[string]any{"time": at.Format(time.RFC3339Nano), "level": level, "msg": message}
	for index := 0; index+1 < len(fields); index += 2 {
		document[fmt.Sprint(fields[index])] = fields[index+1]
	}
	encoded, _ := json.Marshal(document)
	return string(encoded)
}

func caddyLine(at time.Time, level, logger, message string, fields ...any) string {
	document := map[string]any{"level": level, "ts": float64(at.UnixMilli()) / 1000, "msg": message}
	if logger != "" {
		document["logger"] = logger
	}
	for index := 0; index+1 < len(fields); index += 2 {
		document[fmt.Sprint(fields[index])] = fields[index+1]
	}
	encoded, _ := json.Marshal(document)
	return string(encoded)
}

var _ Reader = (*Fake)(nil)
