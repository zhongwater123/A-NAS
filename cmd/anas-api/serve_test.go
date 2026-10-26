package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/zhongwater123/A-NAS/internal/terminal"
)

// A media, music or download stream may outlast the grace period. Stopping
// then closes it and still ends cleanly, so systemd does not record a failed
// stop (#192).
func TestStoppingCutsOffAStreamThatOutlastsTheGrace(t *testing.T) {
	grace := shutdownGrace
	shutdownGrace = 200 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = grace })
	streaming := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(streaming)
		<-r.Context().Done()
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, server, listener, terminal.New(terminal.Config{}, logger), logger) }()

	response, err := http.Get("http://" + listener.Addr().String() + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	<-streaming
	stop()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve() = %v, want a clean stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stopping waited for the stream")
	}
}
