package linux

import (
	"testing"
	"testing/fstest"
)

// The Host Agent parses /proc, .desktop entries and smartctl's output as
// root. These fuzz targets feed each parser arbitrary input: it must return
// a value or an error, never panic (#223).

func FuzzTaskDesktop(f *testing.F) {
	f.Add([]byte("[Desktop Entry]\nType=Application\nExec=/usr/bin/player %U\nIcon=player\n"))
	f.Add([]byte("[Desktop Entry]\nExec=env MODE=1 \"/opt/My App/app\" --x\nIcon=/usr/share/pixmaps/a.png\n[Other]\nExec=x\n"))
	f.Add([]byte("Exec=\"unterminated\nIcon=\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		exe, _ := parseTaskDesktop(data)
		_ = taskDesktopExecutable(exe)
		_ = taskDesktopExecutable(string(data))
	})
}

func FuzzTaskProcess(f *testing.F) {
	f.Add([]byte(string(processStat(42, 10, 20, 3))), []byte("read_bytes: 1\nwrite_bytes: 2\n"))
	f.Add([]byte("42 (a) b) R 1 2 3"), []byte("8:0 rbytes=1 wbytes=2\n"))
	f.Add([]byte(")("), []byte(""))
	f.Fuzz(func(t *testing.T, stat, io []byte) {
		_, _ = parseTaskProcess(stat, 42)
		_, _ = parseProcessIO(io)
		_, _ = parseCgroupIO(io)
	})
}

func FuzzProcMetrics(f *testing.F) {
	f.Add([]byte("cpu  1 2 3 4 5 6 7 8 9 10\ncpu0 1 2 3 4 5 6 7 8 9 10\n"), []byte("MemTotal: 100 kB\nMemAvailable: 50 kB\n"),
		[]byte("Inter-|\n face |\n  eth0: 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16\n"))
	f.Add([]byte("cpu"), []byte("MemTotal:"), []byte("eth0:"))
	f.Fuzz(func(t *testing.T, stat, memory, network []byte) {
		root := fstest.MapFS{
			"proc/stat":    {Data: stat},
			"proc/meminfo": {Data: memory},
			"proc/net/dev": {Data: network},
		}
		_, _, _, _ = readCPU(root)
		_, _ = readMemory(root)
		_, _ = readNetwork(root)
	})
}

func FuzzSMART(f *testing.F) {
	f.Add([]byte(`{"smartctl":{"exit_status":0},"smart_status":{"passed":true},"temperature":{"current":35}}`))
	f.Add([]byte(`{"smartctl":{"exit_status":2,"messages":[{"string":"Device is in STANDBY mode"}]}}`))
	f.Add([]byte(`{"temperature":{"current":-1e309}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseSMART(data)
	})
}
