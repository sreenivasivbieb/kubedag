// Package config loads configuration from environment variables.
//
// Every knob has a documented default so that `make dev-up` works on a laptop
// with zero setup, while a production deployment can override anything.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the full configuration surface of the orchestrator and pod
// manager. Orchestrator-only and pod-manager-only fields are grouped but share
// one struct so that a single binary can run either role in future.
type Config struct {
	// Role selects which process behaviour this config drives.
	Role string

	// --- API ---
	APIAddr string
	// MaxWorkflowBytes bounds the request body for POST /workflow.
	// Bounding input size is a security control, not a nicety.
	MaxWorkflowBytes int64
	// MaxStepsPerWorkflow bounds graph size (F1 requirement).
	MaxStepsPerWorkflow int
	// MaxCommandBytes bounds a single step's argv, to blunt absurd payloads.
	MaxCommandBytes int

	// --- Kubernetes ---
	Kubeconfig  string
	Namespace   string
	RunnerImage string
	// RunnerPodPrefix is the StatefulSet name prefix; pods are
	// <prefix>-0, <prefix>-1, ...
	RunnerPodPrefix string

	// --- Pod pool / registry ---
	// Pool registration mirrors the reference implementation's `workers`
	// table: pods announce themselves and heartbeat, and staleness is the
	// eviction signal. See ADR 0003 for why this replaces lease-as-primary.
	PoolSize int
	// InactivityThreshold is how long a pod may go without a heartbeat
	// before its steps are evicted. The reference repo uses 10s with a 5s
	// tick; we default more conservatively and document the trade-off.
	InactivityThreshold time.Duration
	HeartbeatInterval   time.Duration
	// StalePodTickInterval is how often the eviction sweep runs. Kept
	// comfortably above HeartbeatInterval so a single slow heartbeat does
	// not trigger eviction.
	StalePodTickInterval time.Duration
	// ScheduleTimeout bounds how long a step may sit QUEUED before failing
	// without ever being attempted. Distinct from the execution timeout:
	// schedule timeouts are non-retryable, execution timeouts are retryable.
	ScheduleTimeout time.Duration

	// --- Execution defaults ---
	DefaultStepTimeout time.Duration
	MaxLogBytes        int

	// --- Concurrency ---
	// These bound work in flight. The reference implementation uses a
	// buffered-channel semaphore and passes (slots - len(sem)) into the
	// dequeue LIMIT; we enforce the same bounds in SQL so that multiple
	// orchestrator replicas cannot collectively exceed them.
	GlobalMaxConcurrentSteps int
	PerRunMaxConcurrentSteps int

	// --- Store ---
	DatabaseURL string

	// --- Retry ---
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

// Role names.
const (
	RoleOrchestrator = "orchestrator"
	RolePodManager   = "podmanager"
)

// Load reads configuration from the environment, applying defaults.
// It returns an error rather than panicking so that a misconfigured
// deployment fails loudly at startup instead of at first request.
func Load(role string) (Config, error) {
	c := Config{
		Role: role,

		APIAddr:             env("KUBEDAG_API_ADDR", ":8080"),
		MaxWorkflowBytes:    int64(envInt("KUBEDAG_MAX_WORKFLOW_BYTES", 256*1024)),
		MaxStepsPerWorkflow: envInt("KUBEDAG_MAX_STEPS", 500),
		MaxCommandBytes:     envInt("KUBEDAG_MAX_COMMAND_BYTES", 4096),

		Kubeconfig:      env("KUBEDAG_KUBECONFIG", ""),
		Namespace:       env("KUBEDAG_NAMESPACE", "kubedag"),
		RunnerImage:     env("KUBEDAG_RUNNER_IMAGE", "docker.io/library/busybox:1.36"),
		RunnerPodPrefix: env("KUBEDAG_RUNNER_POD_PREFIX", "runner"),

		PoolSize:             envInt("KUBEDAG_POOL_SIZE", 5),
		InactivityThreshold:  envDuration("KUBEDAG_INACTIVITY_THRESHOLD", 30*time.Second),
		HeartbeatInterval:    envDuration("KUBEDAG_HEARTBEAT_INTERVAL", 5*time.Second),
		StalePodTickInterval: envDuration("KUBEDAG_STALE_POD_TICK_INTERVAL", 10*time.Second),
		ScheduleTimeout:      envDuration("KUBEDAG_SCHEDULE_TIMEOUT", 10*time.Minute),

		DefaultStepTimeout: envDuration("KUBEDAG_DEFAULT_STEP_TIMEOUT", 5*time.Minute),
		MaxLogBytes:        envInt("KUBEDAG_MAX_LOG_BYTES", 64*1024),

		GlobalMaxConcurrentSteps: envInt("KUBEDAG_GLOBAL_MAX_CONCURRENT_STEPS", 20),
		PerRunMaxConcurrentSteps: envInt("KUBEDAG_PER_RUN_MAX_CONCURRENT_STEPS", 8),

		DatabaseURL: env("KUBEDAG_DATABASE_URL", "postgres://kubedag:kubedag@127.0.0.1:5432/kubedag?sslmode=disable"),

		RetryBaseDelay: envDuration("KUBEDAG_RETRY_BASE_DELAY", 2*time.Second),
		RetryMaxDelay:  envDuration("KUBEDAG_RETRY_MAX_DELAY", 5*time.Minute),
	}

	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	if c.Role != RoleOrchestrator && c.Role != RolePodManager {
		return fmt.Errorf("config: KUBEDAG_ROLE must be %q or %q, got %q",
			RoleOrchestrator, RolePodManager, c.Role)
	}
	if c.PoolSize < 1 {
		return fmt.Errorf("config: KUBEDAG_POOL_SIZE must be >= 1, got %d", c.PoolSize)
	}
	// A pod must be able to miss several heartbeats before we declare it
	// dead. If the threshold is too close to the interval, one delayed
	// packet evicts a healthy pod and duplicates work.
	if c.InactivityThreshold <= c.HeartbeatInterval*2 {
		return fmt.Errorf("config: KUBEDAG_INACTIVITY_THRESHOLD (%s) must exceed 2x KUBEDAG_HEARTBEAT_INTERVAL (%s)",
			c.InactivityThreshold, c.HeartbeatInterval)
	}
	// The eviction sweep must be slow enough that a pod which heartbeated
	// just before the sweep is not caught mid-interval.
	if c.StalePodTickInterval < c.HeartbeatInterval {
		return fmt.Errorf("config: KUBEDAG_STALE_POD_TICK_INTERVAL (%s) must be >= KUBEDAG_HEARTBEAT_INTERVAL (%s)",
			c.StalePodTickInterval, c.HeartbeatInterval)
	}
	if c.ScheduleTimeout <= c.DefaultStepTimeout {
		return fmt.Errorf("config: KUBEDAG_SCHEDULE_TIMEOUT (%s) must exceed KUBEDAG_DEFAULT_STEP_TIMEOUT (%s), "+
			"otherwise a step can time out before it is ever picked up",
			c.ScheduleTimeout, c.DefaultStepTimeout)
	}
	if c.PerRunMaxConcurrentSteps > c.GlobalMaxConcurrentSteps {
		return fmt.Errorf("config: KUBEDAG_PER_RUN_MAX_CONCURRENT_STEPS (%d) exceeds KUBEDAG_GLOBAL_MAX_CONCURRENT_STEPS (%d)",
			c.PerRunMaxConcurrentSteps, c.GlobalMaxConcurrentSteps)
	}
	return nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
