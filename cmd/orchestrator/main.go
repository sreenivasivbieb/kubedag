// Command orchestrator is the control plane: HTTP API plus the scheduling
// loop. It is the sole writer of run and step state.
//
// In Phase 0 it serves only the health endpoints; the API surface lands in
// Phase 8 and the scheduling loop in Phase 4. Shipping the binary now means
// the process boundary is real from the first commit, so later phases never
// have to move code across it.
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

	"github.com/sreenivasivbieb/kubedag/internal/config"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("orchestrator exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(config.RoleOrchestrator)
	if err != nil {
		return err
	}

	// Structured JSON from day one. Retro-fitting log format across a
	// codebase is painful; starting here means N5 is a query, not a rewrite.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	slog.Info("starting orchestrator",
		"role", cfg.Role,
		"namespace", cfg.Namespace,
		"pool_size", cfg.PoolSize,
		"api_addr", cfg.APIAddr,
		"go_version", runtimeVersion(),
	)

	// Signal-aware context: Phase 7's graceful drain hangs off this.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              cfg.APIAddr,
		Handler:           routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// A slow client must not be able to pin a connection forever.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func routes() http.Handler {
	mux := http.NewServeMux()

	// healthz: liveness. Answers "is this process wedged?", so it must not
	// depend on Postgres — otherwise a database blip restarts every
	// orchestrator in the cluster and turns a degradation into an outage.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// readyz: readiness. This one *should* check dependencies, and returning
	// 503 removes the pod from the Service without killing it. Postgres is
	// the only dependency: per ADR 0001 there is no queue to check.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ready","checks":{"postgres":"stub"}}`))
	})

	return mux
}

func runtimeVersion() string { return "go1.25.5" }
