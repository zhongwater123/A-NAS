package httpidle_test

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/httpidle"
)

// idle is short so that the tests wait on it a few times over; the steps
// of a transfer that keeps moving take a third of it.
const idle = 300 * time.Millisecond

func serve(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(httpidle.Handler(handler, idle))
	t.Cleanup(server.Close)
	return server.URL
}

// A response that keeps coming for longer than the idle time is sent in
// full, and so is one that the handler takes longer than that to start.
func TestATransferThatKeepsMovingOrStartsLateCompletes(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/late" {
			time.Sleep(2 * idle)
			_, _ = io.WriteString(w, "late")
			return
		}
		for i := range 6 {
			_, _ = fmt.Fprintf(w, "%d", i)
			w.(http.Flusher).Flush()
			time.Sleep(idle / 3)
		}
	})
	for path, want := range map[string]string{"/stream": "012345", "/late": "late"} {
		response, err := http.Get(url + path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
		}
	}
}

// An upload that keeps coming is read in full, and the request is not
// cancelled while the handler works on after the body ended.
func TestAnUploadThatKeepsMovingCompletesAndItsWorkGoesOn(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		time.Sleep(2 * idle)
		if err := r.Context().Err(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, "%d", len(data))
	})
	reader, writer := io.Pipe()
	go func() {
		for range 6 {
			_, _ = writer.Write(bytes.Repeat([]byte("x"), 1000))
			time.Sleep(idle / 3)
		}
		_ = writer.Close()
	}()
	response, err := http.Post(url, "application/octet-stream", reader)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(got) != "6000" {
		t.Fatalf("upload = %d %s", response.StatusCode, got)
	}
}

// A client that stops sending its body, or stops reading the response,
// fails the handler's read or write once the idle time passes.
func TestAStalledClientIsLetGo(t *testing.T) {
	failed := make(chan error, 1)
	url := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, err := io.ReadAll(r.Body)
			failed <- err
			return
		}
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for range 1 << 10 {
			if _, err := w.Write(chunk); err != nil {
				failed <- err
				return
			}
		}
		failed <- errors.New("a gigabyte reached a client that reads nothing")
	})
	address := strings.TrimPrefix(url, "http://")
	for _, request := range []string{
		"POST / HTTP/1.1\r\nHost: test\r\nContent-Length: 100\r\n\r\nonly part of it",
		"GET / HTTP/1.1\r\nHost: test\r\n\r\n",
	} {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-failed:
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Errorf("%q: the handler got %v, want a timeout", strings.Fields(request)[0], err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%q: the handler is still waiting on the client", strings.Fields(request)[0])
		}
		_ = conn.Close()
	}
}

// A WebSocket upgrade reaches the handler with the server's own writer, so
// it can take over the connection.
func TestUpgradesReachTheServersWriter(t *testing.T) {
	url := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\n\r\n")
		_ = buffered.Flush()
	})
	conn, err := net.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(status, "101") {
		t.Fatalf("status line = %q, %v", status, err)
	}
}
