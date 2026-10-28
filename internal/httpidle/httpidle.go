// Package httpidle gives every request of a server as long as it keeps
// moving (#192). Each read of the request body and each write of the
// response must complete within the idle time, so a client that stops
// sending or stops reading frees its connection, and whatever the handler
// holds for it, such as a File Broker descriptor or a media worker slot.
// http.Server's ReadTimeout and WriteTimeout would instead bound a whole
// request, and cut long uploads, downloads and video streams short.
//
// Only waiting on the client counts: the deadline is set just before each
// read or write, so a handler may think as long as it needs to. The
// standard library has no idle timeout per read or write; this sets the
// connection's deadlines through http.ResponseController.
package httpidle

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// Handler serves next with idle as the longest a read of the request body
// or a write of the response may wait on the client. WebSocket upgrades
// pass through: an upgraded connection sets deadlines of its own.
func Handler(next http.Handler, idle time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		controller := http.NewResponseController(w)
		writer := &writer{ResponseWriter: w, controller: controller, idle: idle}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &body{ReadCloser: r.Body, controller: controller, idle: idle}
		}
		next.ServeHTTP(writer, r)
		// The server sends what is still buffered after the handler returns.
		writer.extend()
	})
}

type writer struct {
	http.ResponseWriter
	controller *http.ResponseController
	idle       time.Duration
}

func (w *writer) extend() {
	_ = w.controller.SetWriteDeadline(time.Now().Add(w.idle))
}

func (w *writer) Write(data []byte) (int, error) {
	w.extend()
	return w.ResponseWriter.Write(data)
}

func (w *writer) WriteHeader(status int) {
	w.extend()
	w.ResponseWriter.WriteHeader(status)
}

// Flush serves handlers that assert http.Flusher rather than use a
// ResponseController.
func (w *writer) Flush() {
	w.extend()
	_ = w.controller.Flush()
}

// Unwrap lets http.ResponseController reach the server's writer.
func (w *writer) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

type body struct {
	io.ReadCloser
	controller *http.ResponseController
	idle       time.Duration
}

// Read waits at most idle for the client. Once the body ends, the read
// deadline is cleared: the server then reads ahead to notice the client
// leaving, and a deadline passing there would cancel the request's context
// while the handler is still at work.
func (b *body) Read(p []byte) (int, error) {
	_ = b.controller.SetReadDeadline(time.Now().Add(b.idle))
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		_ = b.controller.SetReadDeadline(time.Time{})
	}
	return n, err
}
