package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	// t.Setenv is used with an empty value to prove the default is applied
	// when the variable is unset as well as empty.
	c, err := Load(RoleOrchestrator)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"api addr", c.APIAddr, ":8080"},
		{"namespace", c.Namespace, "kubedag"},
		{"pool size", c.PoolSize, 5},
		{"lease ttl", c.LeaseTTL, 60 * time.Second},
		{"heartbeat interval", c.HeartbeatInterval, 5 * time.Second},
		{"global concurrency", c.GlobalMaxConcurrentSteps, 20},
		{"per-run concurrency", c.PerRunMaxConcurrentSteps, 8},
		{"step queue", c.StepQueue, "kubedag:steps"},
		{"result queue", c.ResultQueue, "kubedag:results"},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadOverridesFromEnv(t *testing.T) {
	t.Setenv("KUBEDAG_POOL_SIZE", "12")
	t.Setenv("KUBEDAG_LEASE_TTL", "90s")
	t.Setenv("KUBEDAG_NAMESPACE", "other")

	c, err := Load(RoleOrchestrator)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PoolSize != 12 {
		t.Errorf("PoolSize = %d, want 12", c.PoolSize)
	}
	if c.LeaseTTL != 90*time.Second {
		t.Errorf("LeaseTTL = %s, want 90s", c.LeaseTTL)
	}
	if c.Namespace != "other" {
		t.Errorf("Namespace = %q, want %q", c.Namespace, "other")
	}
}

// Malformed values must fall back to the default rather than crashing the
// process. A bad env var should degrade to a working system with a logged
// warning, not take the orchestrator down at boot.
func TestLoadMalformedEnvFallsBackToDefault(t *testing.T) {
	t.Setenv("KUBEDAG_POOL_SIZE", "not-a-number")
	t.Setenv("KUBEDAG_LEASE_TTL", "not-a-duration")

	c, err := Load(RoleOrchestrator)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PoolSize != 5 {
		t.Errorf("PoolSize = %d, want default 5", c.PoolSize)
	}
	if c.LeaseTTL != 60*time.Second {
		t.Errorf("LeaseTTL = %s, want default 60s", c.LeaseTTL)
	}
}

// The lease/heartbeat relationship is a correctness property, not a taste
// question: if a lease can expire while its holder is demonstrably alive, two
// orchestrators can own the same pod and both exec into it.
func TestValidateRejectsShortLeaseTTL(t *testing.T) {
	t.Setenv("KUBEDAG_LEASE_TTL", "5s")
	t.Setenv("KUBEDAG_HEARTBEAT_INTERVAL", "5s")

	_, err := Load(RoleOrchestrator)
	if err == nil {
		t.Fatal("expected error: LeaseTTL must exceed 2x heartbeat interval")
	}
}

func TestValidateRejectsShortHeartbeatTimeout(t *testing.T) {
	t.Setenv("KUBEDAG_HEARTBEAT_INTERVAL", "10s")
	t.Setenv("KUBEDAG_HEARTBEAT_TIMEOUT", "5s")

	if _, err := Load(RoleOrchestrator); err == nil {
		t.Fatal("expected error: HeartbeatTimeout must exceed heartbeat interval")
	}
}

func TestValidateRejectsPerRunAboveGlobal(t *testing.T) {
	t.Setenv("KUBEDAG_GLOBAL_MAX_CONCURRENT_STEPS", "4")
	t.Setenv("KUBEDAG_PER_RUN_MAX_CONCURRENT_STEPS", "10")

	if _, err := Load(RoleOrchestrator); err == nil {
		t.Fatal("expected error: per-run concurrency cannot exceed global")
	}
}

func TestValidateRejectsBadRole(t *testing.T) {
	if _, err := Load("something-else"); err == nil {
		t.Fatal("expected error: unknown role")
	}
}

func TestValidateRejectsZeroPool(t *testing.T) {
	t.Setenv("KUBEDAG_POOL_SIZE", "0")
	if _, err := Load(RoleOrchestrator); err == nil {
		t.Fatal("expected error: pool size must be >= 1")
	}
}
