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

	// --- Pod pool ---
	PoolSize          int
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	// HeartbeatTimeout is how many consecutive missed heartbeats before a
	// step is declared lost.
	HeartbeatTimeout time.Duration

	// --- Execution defaults ---
	DefaultStepTimeout time.Duration
	MaxLogBytes        int

	// --- Concurrency ---
	// GlobalMaxConcurrentSteps bounds steps in flight across all runs.
	GlobalMaxConcurrentSteps int
	// PerRunMaxConcurrentSteps bounds steps in flight within one run.
	PerRunMaxConcurrentSteps int

	// --- Queue ---
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	StepQueue     string
	ResultQueue   string
	// QueuePollInterval is the orchestrator's idle tick. We BRPOP with a
	// timeout rather than hot-looping.
	QueuePollInterval time.Duration

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

		PoolSize:          envInt("KUBEDAG_POOL_SIZE", 5),
		LeaseTTL:          envDuration("KUBEDAG_LEASE_TTL", 60*time.Second),
		HeartbeatInterval: envDuration("KUBEDAG_HEARTBEAT_INTERVAL", 5*time.Second),
		HeartbeatTimeout:  envDuration("KUBEDAG_HEARTBEAT_TIMEOUT", 30*time.Second),

		DefaultStepTimeout: envDuration("KUBEDAG_DEFAULT_STEP_TIMEOUT", 5*time.Minute),
		MaxLogBytes:        envInt("KUBEDAG_MAX_LOG_BYTES", 64*1024),

		GlobalMaxConcurrentSteps: envInt("KUBEDAG_GLOBAL_MAX_CONCURRENT_STEPS", 20),
		PerRunMaxConcurrentSteps: envInt("KUBEDAG_PER_RUN_MAX_CONCURRENT_STEPS", 8),

		RedisAddr:         env("KUBEDAG_REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:     env("KUBEDAG_REDIS_PASSWORD", ""),
		RedisDB:           envInt("KUBEDAG_REDIS_DB", 0),
		StepQueue:         env("KUBEDAG_STEP_QUEUE", "kubedag:steps"),
		ResultQueue:       env("KUBEDAG_RESULT_QUEUE", "kubedag:results"),
		QueuePollInterval: envDuration("KUBEDAG_QUEUE_POLL_INTERVAL", time.Second),

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
	// A lease must outlive at least two heartbeat intervals, otherwise a
	// lease can expire while the holder is demonstrably alive, which would
	// hand a live pod to two owners.
	if c.LeaseTTL <= c.HeartbeatInterval*2 {
		return fmt.Errorf("config: KUBEDAG_LEASE_TTL (%s) must exceed 2x KUBEDAG_HEARTBEAT_INTERVAL (%s)",
			c.LeaseTTL, c.HeartbeatInterval)
	}
	if c.HeartbeatTimeout <= c.HeartbeatInterval {
		return fmt.Errorf("config: KUBEDAG_HEARTBEAT_TIMEOUT (%s) must exceed KUBEDAG_HEARTBEAT_INTERVAL (%s)",
			c.HeartbeatTimeout, c.HeartbeatInterval)
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
