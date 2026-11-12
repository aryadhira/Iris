// irisd is the Iris backend: rooms, messages, auth and the Hermes proxy.
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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aryadhira/iris/internal/auth"
	"github.com/aryadhira/iris/internal/config"
	"github.com/aryadhira/iris/internal/hermes"
	"github.com/aryadhira/iris/internal/httpapi"
	"github.com/aryadhira/iris/internal/store"
	irisvoice "github.com/aryadhira/iris/internal/voice"
)

const version = "0.1.0-p4"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "irisd:", err)
		os.Exit(1)
	}
}

func run() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	bootCtx, bootCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer bootCancel()

	pool, err := pgxpool.New(bootCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("cannot configure the database: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(bootCtx); err != nil {
		return fmt.Errorf("cannot reach the database: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("cannot create IRIS_DATA_DIR at %s: %w", cfg.DataDir, err)
	}
	var roomsTable *string
	if err := pool.QueryRow(bootCtx, "SELECT to_regclass('public.rooms')::text").Scan(&roomsTable); err != nil {
		return fmt.Errorf("cannot inspect the database schema: %w", err)
	}
	if roomsTable == nil {
		return errors.New("the database has no Iris schema; run `migrate -path db/migrations -database \"$IRIS_DATABASE_URL\" up` first")
	}

	queries := store.New(pool)
	authService := auth.New(queries, cfg.Passphrase, cfg.CookieSecure, logger)
	hermesClient := hermes.New(cfg.HermesURL, cfg.HermesKey, logger)
	voiceClient := irisvoice.New(cfg.VoiceURL, logger)

	e := httpapi.New(httpapi.Options{
		Store:            queries,
		Hermes:           hermesClient,
		Voice:            voiceClient,
		Auth:             authService,
		Logger:           logger,
		Version:          version,
		DataDir:          cfg.DataDir,
		WebDir:           cfg.WebDir,
		RunContinuations: true,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           e,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("irisd listening", "addr", cfg.Addr, "hermes", cfg.HermesURL, "voice", cfg.VoiceURL, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-errCh:
		return fmt.Errorf("http server failed: %w", err)
	case <-sigCtx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	}
}
