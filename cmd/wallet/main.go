// Command wallet runs the Wallet & Payment service.
//
// Usage:
//
//	wallet serve    start the HTTP server
//	wallet migrate  apply pending database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Johna210/wallet_and_payment/internal/app"
	"github.com/Johna210/wallet_and_payment/internal/httpapi"
	"github.com/Johna210/wallet_and_payment/internal/platform/config"
	"github.com/Johna210/wallet_and_payment/internal/platform/logging"
	"github.com/Johna210/wallet_and_payment/internal/store/postgres"
)

const usage = "usage: wallet <serve|migrate>"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	logger := logging.New(os.Stdout, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch os.Args[1] {
	case "serve":
		err = serve(ctx, cfg, logger)
	case "migrate":
		err = migrate(ctx, cfg, logger)
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		logger.Error("exiting with error", slog.String("command", os.Args[1]), slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func migrate(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()
	return postgres.Migrate(ctx, pool, logger)
}

// serve runs the HTTP server until ctx is cancelled by SIGINT or SIGTERM, then
// shuts down gracefully: readiness fails first so traffic drains away, the
// server stops accepting connections and waits for in-flight requests, and
// only then is the pool closed.
func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	readiness := httpapi.NewReadiness(pool)
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           app.NewHandler(cfg, logger, pool, readiness),
		ReadHeaderTimeout: cfg.HTTPReadTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown started: readiness now failing", slog.Duration("drain_delay", cfg.ShutdownDelay))
	readiness.SetShuttingDown()
	time.Sleep(cfg.ShutdownDelay)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("shutdown complete")
	return nil
}
