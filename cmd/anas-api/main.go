package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zhongwater123/A-NAS/internal/hoststate"
	"github.com/zhongwater123/A-NAS/internal/hoststate/agent"
	"github.com/zhongwater123/A-NAS/internal/hoststate/fake"
	"github.com/zhongwater123/A-NAS/internal/httpapi"
	"github.com/zhongwater123/A-NAS/internal/terminal"
	"github.com/zhongwater123/A-NAS/internal/webui"
)

var version = "dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("A-NAS API stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	address := os.Getenv("ANAS_HTTP_ADDR")
	if address == "" {
		address = "127.0.0.1:8080"
	}

	reader, dataSource, err := configuredReader()
	if err != nil {
		return err
	}

	terminalEnabled, err := configuredTerminal()
	if err != nil {
		return err
	}
	terminals := terminal.New(terminal.Config{Enabled: terminalEnabled}, logger)

	apiHandler := withTerminal(httpapi.New(reader, dataSource, version, logger), terminals)
	handler, err := webui.New(apiHandler)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	listenError := make(chan error, 1)
	go func() {
		logger.Info("A-NAS API listening", "address", address, "version", version, "terminal", terminalEnabled)
		listenError <- server.ListenAndServe()
	}()

	select {
	case err := <-listenError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("A-NAS API shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if err := terminals.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-listenError
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func configuredReader() (hoststate.Reader, httpapi.DataSource, error) {
	switch mode := os.Getenv("ANAS_HOSTSTATE_MODE"); mode {
	case "", "fake":
		return fake.NewHealthy(), httpapi.DataSourceSimulated, nil
	case "agent":
		socketPath, err := agent.SocketPath()
		if err != nil {
			return nil, "", err
		}
		return agent.NewClient(socketPath), httpapi.DataSourceLive, nil
	default:
		return nil, "", errors.New("ANAS_HOSTSTATE_MODE must be fake or agent")
	}
}

func configuredTerminal() (bool, error) {
	switch mode := os.Getenv("ANAS_TERMINAL"); mode {
	case "", "disabled":
		return false, nil
	case "enabled":
		return true, nil
	default:
		return false, errors.New("ANAS_TERMINAL must be enabled or disabled")
	}
}

func withTerminal(api http.Handler, terminals http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == terminal.StatusPath || r.URL.Path == terminal.SessionPath {
			terminals.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}
