// Command podmanager is the data plane. It leases pods, execs commands, and
// reports outcomes. It never writes run or step state.
//
// This is the invariant that makes the whole system tractable: exactly one
// component is allowed to decide what a run's state is. The pod manager
// reports *observations* (this command exited 137 on that pod); the
// orchestrator decides *meaning* (that makes the step FAILED).
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sreenivasivbieb/kubedag/internal/config"
	"github.com/sreenivasivbieb/kubedag/internal/runtime"
)

func main() {
	if err := run(); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("podmanager exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(config.RolePodManager)
	if err != nil {
		return err
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The Kubernetes-backed Runtime arrives in Phase 3. Until then we hold
	// the interface and refuse to start loudly rather than pretending to be
	// a working data plane: a stub that silently succeeds would let the rest
	// of the system be developed against a fiction.
	rt, err := buildRuntime(ctx, cfg)
	if err != nil {
		return err
	}
	defer rt.Close()

	slog.Info("starting podmanager",
		"namespace", cfg.Namespace,
		"pool_size", cfg.PoolSize,
		"lease_ttl", cfg.LeaseTTL.String(),
	)

	// The dequeue loop arrives in Phase 4.
	<-ctx.Done()
	slog.Info("shutdown signal received")
	return nil
}

// buildRuntime is the single construction point for the Runtime
// implementation. Swapping Kubernetes for a local process runner, or for a
// fake in tests, means changing this function and nothing else.
func buildRuntime(ctx context.Context, cfg config.Config) (runtime.Runtime, error) {
	return nil, errors.New("podmanager: no Runtime implementation registered yet (Phase 3)")
}
