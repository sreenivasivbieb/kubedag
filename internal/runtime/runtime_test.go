package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/sreenivasivbieb/kubedag/internal/domain"
)

// Heartbeat staleness is the eviction signal (ADR 0003).
//
// The threshold is INCLUSIVE of safety: a pod exactly at the threshold is not
// yet stale. The asymmetry decides it. Evicting one tick early causes
// duplicate side effects, which is real user-visible harm. Evicting one tick
// late costs a few seconds of delay, which is invisible. When the two failure
// modes cost different amounts, the threshold should be conservative.
func TestRunnerPodIsStale(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const threshold = 30 * time.Second

	tests := []struct {
		name          string
		lastHeartbeat time.Time
		want          bool
	}{
		{"heartbeat just now", now, false},
		{"inside threshold", now.Add(-29 * time.Second), false},
		{"exactly at threshold is not yet stale", now.Add(-30 * time.Second), false},
		{"just past threshold is stale", now.Add(-31 * time.Second), true},
		{"well past threshold", now.Add(-time.Hour), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := domain.RunnerPod{LastHeartbeatAt: tc.lastHeartbeat}
			if got := p.IsStale(now, threshold); got != tc.want {
				t.Errorf("IsStale() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A heartbeat proves the pod is alive, NOT that the step is progressing. This
// test pins that distinction so nobody later "fixes" the watchdog by treating
// a heartbeating pod as proof of progress. A step wedged in user code will
// heartbeat forever; only the per-step timeout can save the run.
func TestHeartbeatDoesNotImplyProgress(t *testing.T) {
	now := time.Now()
	p := domain.RunnerPod{LastHeartbeatAt: now}

	if p.IsStale(now, 30*time.Second) {
		t.Fatal("a heartbeating pod must never be reported stale, " +
			"even if the step it runs is hung; that is the timeout's job")
	}
}

func TestPodHandleAvailable(t *testing.T) {
	tests := []struct {
		name string
		h    PodHandle
		want bool
	}{
		{"first slot", PodHandle{Slot: 0, Slots: 4}, true},
		{"last slot", PodHandle{Slot: 3, Slots: 4}, true},
		{"slot past capacity", PodHandle{Slot: 4, Slots: 4}, false},
		{"negative slot", PodHandle{Slot: -1, Slots: 4}, false},
		{"no slots configured", PodHandle{Slot: 0, Slots: 0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.h.Available(); got != tc.want {
				t.Errorf("Available() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodInfoIdle(t *testing.T) {
	tests := []struct {
		name string
		p    PodInfo
		want bool
	}{
		{"ready with free slot", PodInfo{Ready: true, Slots: 2, ActiveSteps: 0}, true},
		{"ready at capacity", PodInfo{Ready: true, Slots: 2, ActiveSteps: 2}, false},
		{"not ready", PodInfo{Ready: false, Slots: 2, ActiveSteps: 0}, false},
		{"ready but no slots configured", PodInfo{Ready: true, Slots: 0, ActiveSteps: 0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Idle(); got != tc.want {
				t.Errorf("Idle() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecResultSucceeded(t *testing.T) {
	tests := []struct {
		name string
		res  ExecResult
		want bool
	}{
		{"exit zero", ExecResult{ExitCode: 0}, true},
		{"nonzero exit", ExecResult{ExitCode: 1}, false},
		{"killed by signal 137", ExecResult{ExitCode: 137}, false},
		{"timed out", ExecResult{TimedOut: true}, false},
		{"infra error", ExecResult{Err: errors.New("pod gone")}, false},
		{"infra error despite exit zero", ExecResult{ExitCode: 0, Err: errors.New("stream broke")}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Succeeded(); got != tc.want {
				t.Errorf("Succeeded() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A timeout and a non-zero exit are different verdicts. Conflating them leaves
// Phase 6's retry classifier unable to distinguish "the user's program said
// no" from "we killed it for running too long", which carry different retry
// policies: the first is usually not retryable, the second usually is.
func TestExecResultOutcome(t *testing.T) {
	tests := []struct {
		name string
		res  ExecResult
		want string
	}{
		{"clean success", ExecResult{ExitCode: 0}, "SUCCEEDED"},
		{"user program failed", ExecResult{ExitCode: 2}, "FAILED"},
		{"timeout", ExecResult{ExitCode: -1, TimedOut: true}, "TIMED_OUT"},
		{"infra failure", ExecResult{Err: errors.New("node not found")}, "FAILED"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(tc.res.Outcome()); got != tc.want {
				t.Errorf("Outcome() = %s, want %s", got, tc.want)
			}
		})
	}
}
