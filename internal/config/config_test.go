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
		{"inactivity threshold", c.InactivityThreshold, 30 * time.Second},
		{"heartbeat interval", c.HeartbeatInterval, 5 * time.Second},
		{"global concurrency", c.GlobalMaxConcurrentSteps, 20},
		{"per-run concurrency", c.PerRunMaxConcurrentSteps, 8},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadOverridesFromEnv(t *testing.T) {
	t.Setenv("KUBEDAG_POOL_SIZE", "12")
	t.Setenv("KUBEDAG_INACTIVITY_THRESHOLD", "90s")
	t.Setenv("KUBEDAG_NAMESPACE", "other")

	c, err := Load(RoleOrchestrator)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PoolSize != 12 {
		t.Errorf("PoolSize = %d, want 12", c.PoolSize)
	}
	if c.InactivityThreshold != 90*time.Second {
		t.Errorf("InactivityThreshold = %s, want 90s", c.InactivityThreshold)
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
	t.Setenv("KUBEDAG_INACTIVITY_THRESHOLD", "not-a-duration")

	c, err := Load(RoleOrchestrator)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PoolSize != 5 {
		t.Errorf("PoolSize = %d, want default 5", c.PoolSize)
	}
	if c.InactivityThreshold != 30*time.Second {
		t.Errorf("InactivityThreshold = %s, want default 30s", c.InactivityThreshold)
	}
}

// The inactivity-threshold/heartbeat relationship is a correctness property,
// not a taste question: if the threshold is too close to the interval, one
// delayed packet evicts a healthy pod and duplicates work.
func TestValidateRejectsShortInactivityThreshold(t *testing.T) {
	t.Setenv("KUBEDAG_INACTIVITY_THRESHOLD", "5s")
	t.Setenv("KUBEDAG_HEARTBEAT_INTERVAL", "5s")

	_, err := Load(RoleOrchestrator)
	if err == nil {
		t.Fatal("expected error: InactivityThreshold must exceed 2x heartbeat interval")
	}
}

func TestValidateRejectsFastStaleSweep(t *testing.T) {
	t.Setenv("KUBEDAG_HEARTBEAT_INTERVAL", "10s")
	t.Setenv("KUBEDAG_STALE_POD_TICK_INTERVAL", "5s")

	if _, err := Load(RoleOrchestrator); err == nil {
		t.Fatal("expected error: stale sweep tick must be >= heartbeat interval")
	}
}

// Schedule timeout must exceed execution timeout, otherwise a step can be
// failed for being slow before it was ever picked up.
func TestValidateRejectsShortScheduleTimeout(t *testing.T) {
	t.Setenv("KUBEDAG_SCHEDULE_TIMEOUT", "10s")
	t.Setenv("KUBEDAG_DEFAULT_STEP_TIMEOUT", "5m")

	if _, err := Load(RoleOrchestrator); err == nil {
		t.Fatal("expected error: schedule timeout must exceed default step timeout")
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
