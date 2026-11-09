package assistantapi

import (
	"net/http"
	"time"

	"github.com/zhongwater123/A-NAS/internal/httpjson"
)

// The companion tools of members' assistants (#412) reach the Product Service
// on a socket of their own, which systemd binds into each assistant's sandbox
// (ai/assistant-spike/anas-companion-assistant.socket); the scheduled tasks'
// sandboxes do not have it. The tools move and animate the companion in the
// VNAS window of the conversation that called them. Until the Product Service
// passes their commands on, every call is told that no desktop is in use.

// CompanionServer serves the companion tools' socket: POST /call, nothing else.
func (h *Handler) CompanionServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.Write(w, http.StatusOK, map[string]string{"status": "no_desktop"})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		httpjson.WriteError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
}
