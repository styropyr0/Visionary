// Command visionary serves dashboards and an AI analyst over the D1 request log
// and system metrics written by the API server.
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

	"github.com/joho/godotenv"
	// Time zone data for the analyst; distroless images have none.
	_ "time/tzdata"

	"github.com/styropyr0/visionary/internal/analytics"
	"github.com/styropyr0/visionary/internal/analytics/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	// A local .env is optional; containers pass real environment variables.
	_ = godotenv.Load()

	cfg, err := analytics.LoadConfig()
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	srv, err := analytics.New(cfg, logger, web.FS())
	if err != nil {
		logger.Error("start visionary", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// D1 runs each statement as its own HTTP call, and a first upgrade adds
	// dozens of columns, so this gets a generous budget. Whatever fails is
	// retried in the background once the server is up.
	initCtx, cancel := context.WithTimeout(ctx, time.Minute)
	if err := srv.Init(initCtx); err != nil {
		logger.Error("create tables; retrying in the background", "err", err)
	}
	cancel()
	// Background work: alert checks, rollups, digests and the like.
	srv.Start(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// Prompt answers can take several model round-trips.
		WriteTimeout: 4 * time.Minute,
		IdleTimeout:  2 * time.Minute,
	}

	go func() {
		logger.Info("visionary listening", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}
