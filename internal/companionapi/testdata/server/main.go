// This loopback-only browser test fixture supplies one synthetic member. It is
// not an authentication implementation or a product server; never deploy it.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/zhongwater123/A-NAS/internal/accounts"
	"github.com/zhongwater123/A-NAS/internal/companion"
	"github.com/zhongwater123/A-NAS/internal/companionapi"
)

func main() {
	if len(os.Args) != 2 {
		panic("test fixture requires an existing disposable directory")
	}
	info, err := os.Stat(os.Args[1])
	if err != nil || !info.IsDir() {
		panic("invalid test directory")
	}
	store, err := companion.Open(filepath.Join(os.Args[1], "companion.db"), companion.Options{})
	if err != nil {
		panic(err)
	}
	defer func() { _ = store.Close() }()
	mux := http.NewServeMux()
	for _, route := range companionapi.New(store, slog.New(slog.NewTextHandler(io.Discard, nil))).Routes() {
		mux.HandleFunc(route.Method+" "+route.Pattern, func(w http.ResponseWriter, r *http.Request) {
			route.Handler(w, r, accounts.Session{User: accounts.User{ID: "browser-test-member"}})
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); _ = server.Close() }()
	fmt.Println("http://" + listener.Addr().String())
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
