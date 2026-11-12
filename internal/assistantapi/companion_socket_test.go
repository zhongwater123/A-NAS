package assistantapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// socketWait is how long a test waits for the companion socket to answer or
// close a connection the bounds below cut short.
const socketWait = 5 * time.Second

// companionSocket serves a companion socket for alice, running as the test's
// own UID, and returns its path and a client of it.
func companionSocket(t *testing.T) (string, *http.Client) {
	t.Helper()
	return companionSocketOf(t, New(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
}

// companionSocketOf serves api's companion socket, with alice running as the
// test's own UID.
func companionSocketOf(t *testing.T, api *Handler) (string, *http.Client) {
	t.Helper()
	uid := os.Getuid()
	if uid == 0 {
		t.Skip("root at the other end is a wiring fault, not a member")
	}
	api.options.Accounts = uids{uid: "alice"}
	dir, err := os.MkdirTemp("", "cs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "c.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := api.CompanionServer()
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
	t.Cleanup(client.CloseIdleConnections)
	return path, client
}

// rawCall opens a connection to the socket and writes request on it.
func rawCall(t *testing.T, path, request string) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if request != "" {
		if _, err := io.WriteString(conn, request); err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

// closedSoon fails unless the server closes conn within socketWait.
func closedSoon(t *testing.T, conn net.Conn, reader *bufio.Reader) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(socketWait))
	if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("the connection is still open: %v", err)
	}
}

func setFor[T any](t *testing.T, variable *T, value T) {
	t.Helper()
	old := *variable
	*variable = value
	t.Cleanup(func() { *variable = old })
}

const wakeCall = `{"tool":"wake","args":{}}`

func TestAUIDsConnectionsAreBoundedBeforeAnythingIsRead(t *testing.T) {
	path, client := companionSocket(t)
	// Connections that send nothing take every place the UID has...
	var held []net.Conn
	for range maxCompanionConnections {
		held = append(held, rawCall(t, path, ""))
	}
	// ...so the next is answered busy and closed, without being read.
	conn := rawCall(t, path, "")
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(socketWait))
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("a connection over the bound: %v", err)
	}
	var data map[string]any
	_ = json.NewDecoder(response.Body).Decode(&data)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || data["status"] != "busy" || data["reason"] != "rate-limited" {
		t.Fatalf("a connection over the bound: %d %v", response.StatusCode, data)
	}
	closedSoon(t, conn, reader)

	// Once one of them closes, a call gets through again.
	_ = held[0].Close()
	deadline := time.Now().Add(socketWait)
	for {
		response, err := client.Post("http://companion/call", "application/json", strings.NewReader(wakeCall))
		if err != nil {
			t.Fatal(err)
		}
		data = nil
		_ = json.NewDecoder(response.Body).Decode(&data)
		_ = response.Body.Close()
		if data["status"] == "no_desktop" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after a connection closed: %v", data)
		}
		client.CloseIdleConnections()
		time.Sleep(20 * time.Millisecond)
	}
}

func TestACallOverTheBoundSentAtOnceStillReadsBusy(t *testing.T) {
	// The companion server sends its call as soon as it has connected. The
	// refusal closes the connection before or after the call goes out, and
	// either way its answer waits to be read: anas_companion_mcp.py reads it
	// after a send that failed, rather than report a timeout.
	path, _ := companionSocket(t)
	for range maxCompanionConnections {
		rawCall(t, path, "")
	}
	call := "POST /call HTTP/1.1\r\nHost: companion\r\nContent-Type: application/json\r\nContent-Length: " +
		strconv.Itoa(len(wakeCall)) + "\r\n\r\n" + wakeCall
	for i := range 20 {
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		if i%2 == 1 {
			// Long enough for the refusal to have closed the connection.
			time.Sleep(20 * time.Millisecond)
		}
		_, _ = io.WriteString(conn, call)
		_ = conn.SetReadDeadline(time.Now().Add(socketWait))
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("call %d over the bound: %v", i, err)
		}
		var data map[string]any
		_ = json.NewDecoder(response.Body).Decode(&data)
		_ = response.Body.Close()
		_ = conn.Close()
		if data["status"] != "busy" {
			t.Fatalf("call %d over the bound: %d %v", i, response.StatusCode, data)
		}
	}
}

func TestACallsHeadersAreSmall(t *testing.T) {
	path, _ := companionSocket(t)
	pad := strings.Repeat("a", 16<<10)
	conn := rawCall(t, path, "POST /call HTTP/1.1\r\nHost: companion\r\nContent-Type: application/json\r\nX-Pad: "+pad+
		"\r\nContent-Length: "+strconv.Itoa(len(wakeCall))+"\r\n\r\n"+wakeCall)
	_ = conn.SetReadDeadline(time.Now().Add(socketWait))
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("16 KiB of headers: %d", response.StatusCode)
	}
}

func TestABodyNobodyReadsIsWaitedForOnlyAsLongAsTheReadTimeout(t *testing.T) {
	// The route the socket does not serve answers without reading the body;
	// net/http then reads what is left of it before the answer goes out, and
	// a caller that never sends it would hold the connection for good.
	setFor(t, &companionHeaders, 500*time.Millisecond)
	path, _ := companionSocket(t)
	conn := rawCall(t, path, "POST /other HTTP/1.1\r\nHost: companion\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n")
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(socketWait))
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("a body that never comes: %v", err)
	}
	// Close leaves the body of an answer that closes the connection unread.
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("a body that never comes: %d", response.StatusCode)
	}
	closedSoon(t, conn, reader)
}

func TestAnIdleConnectionClosesSoon(t *testing.T) {
	setFor(t, &companionIdle, 300*time.Millisecond)
	path, _ := companionSocket(t)
	conn := rawCall(t, path, "POST /call HTTP/1.1\r\nHost: companion\r\nContent-Type: application/json\r\nContent-Length: "+
		strconv.Itoa(len(wakeCall))+"\r\n\r\n"+wakeCall)
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(socketWait))
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Close {
		t.Fatalf("a call: %d, close %v", response.StatusCode, response.Close)
	}
	closedSoon(t, conn, reader)
}

func TestTheReadTimeoutDoesNotCutACallShortOnceItsBodyIsRead(t *testing.T) {
	// A call waits for its tool.start and its window's answer after its body,
	// well past the whole-request read timeout: that bounds reading the call,
	// not the call, and must neither cancel it nor cut its answer off.
	setFor(t, &companionHeaders, 100*time.Millisecond)
	setFor(t, &companionBody, 100*time.Millisecond)
	h := newHarness(t)
	_, client := companionSocketOf(t, h.api)
	w, conn := companionTurn(t, h)
	results := make(chan map[string]any, 1)
	go func() {
		var data map[string]any
		if response, err := client.Post("http://companion/call", "application/json", strings.NewReader(wakeCall)); err == nil {
			_ = json.NewDecoder(response.Body).Decode(&data)
			_ = response.Body.Close()
		}
		results <- data
	}()
	time.Sleep(2 * (companionHeaders + companionBody))
	h.started(conn, fakeRuntime, "call-1", "wake", `{}`)
	sent := command(w)
	answer(w, fakeRuntime, sent.Payload.CommandID, `"ok":true,"result":{"status":"ok","sleeping":false,"changed":true}`)
	if status := statusOf(t, results); status != "ok" {
		t.Fatalf("a call past the read timeout: %s", status)
	}
}
