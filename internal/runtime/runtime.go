// Package runtime defines the contract between the orchestrator's scheduling
// logic and whatever actually executes a step's code.
//
// This is the most important boundary in kubedag. Everything above it (DAG
// maths, state transitions, retry policy) is pure and testable; everything
// below it is infrastructure that can fail in creative ways. Keeping the
// interface narrow and the implementations swappable is what lets us test
// failure modes deterministically instead of by breaking a real cluster.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sreenivasivbieb/kubedag/internal/domain"
)

var (
	// ErrNoPodAvailable means the pool is empty and cannot scale fast enough
	// to satisfy the request before its own deadline. Implementations return
	// this rather than blocking indefinitely, so the orchestrator retains
	// control of backpressure and re-queueing.
	ErrNoPodAvailable = errors.New("runtime: no pod available")
	// ErrPodLost means the pod vanished mid-execution. Classified as
	// retryable: the work did not fail, the infrastructure did.
	ErrPodLost = errors.New("runtime: pod lost")
	// ErrSlotUnavailable means the pod is alive but already at its
	// concurrency budget.
	ErrSlotUnavailable = errors.New("runtime: pod at capacity")
)

// PodHandle is a reference to a registered runner pod and the slot reserved on
// it.
//
// This replaces the lease-with-monotonic-fencing-token design of the original
// Phase 0. The reason is in ADR 0003: fencing is derived from
// (attempt, pod_id) in the database rather than from a counter that must be
// allocated and kept consistent. Pod liveness is carried by heartbeats rather
// than by lease TTL, because a TTL cannot distinguish a slow pod from a dead
// one.
type PodHandle struct {
	PodID   string `json:"podId"`
	PodName string `json:"podName"`
	Node    string `json:"node"`
	// Slot is the concurrency slot reserved on this pod, in [0, Slots).
	Slot  int `json:"slot"`
	Slots int `json:"slots"`
}

// Available reports whether the handle refers to a live pod with capacity.
func (h PodHandle) Available() bool { return h.Slot >= 0 && h.Slot < h.Slots }

// ExecResult is the outcome of one attempt as observed by the runtime.
//
// Note what is *absent*: there is no field for "is this result still
// relevant". Freshness is decided by the orchestrator comparing Attempt and
// PodID against the row (see domain.Fence), not by the pod. A pod that was
// evicted and came back must not be able to resurrect a stale result by
// virtue of being alive.
type ExecResult struct {
	Pod       string
	Node      string
	ExitCode  int
	Stdout    string
	Stderr    string
	StartedAt time.Time
	EndedAt   time.Time
	Duration  time.Duration
	// TimedOut is set when the runtime killed the command because it
	// exceeded its budget. Distinct from ExitCode != 0, because a timeout is
	// an infrastructure verdict rather than the user's program speaking.
	TimedOut bool
	// Err carries infrastructure-level failure (lost pod, transport error)
	// as opposed to the command itself failing.
	Err error
}

// Succeeded reports whether the command ran to completion with exit code 0.
func (r ExecResult) Succeeded() bool {
	return r.Err == nil && !r.TimedOut && r.ExitCode == 0
}

// Outcome maps the raw result onto a step status.
func (r ExecResult) Outcome() domain.StepStatus {
	switch {
	case r.Err != nil:
		return domain.StepFailed
	case r.TimedOut:
		return domain.StepTimedOut
	case r.ExitCode == 0:
		return domain.StepSucceeded
	default:
		return domain.StepFailed
	}
}

// Request is one unit of work handed to the runtime.
type Request struct {
	RunID    string
	StepID   string
	StepSpec domain.StepSpec
	// RunEnv is merged beneath StepSpec.Env. Step-level wins.
	RunEnv map[string]string
	// AttemptID and Attempt are injected into the container environment so
	// user code can deduplicate its own side effects (at-least-once delivery
	// is all we can offer; idempotency is the user's job, and we make it
	// possible). Attempt also forms half of the fence — see ADR 0003.
	AttemptID string
	Attempt   int
	// Timeout bounds this single attempt.
	Timeout time.Duration
	// MaxLogBytes bounds captured stdout/stderr. Output is truncated with a
	// marker rather than dropped, so the operator can see that it happened.
	MaxLogBytes int
}

// PodInfo describes a pod in the runner pool.
type PodInfo struct {
	Name string
	Node string
	// Ready reports kubelet readiness.
	Ready bool
	// Phase is the Kubernetes pod phase string.
	Phase string
	// LastHeartbeat is the control-plane view of pod liveness. This is
	// authoritative over the Kubernetes phase: a pod can be Running and
	// still be wedged, or Pending for reasons the API will not explain.
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	Slots         int       `json:"slots"`
	ActiveSteps   int       `json:"activeSteps"`
}

// Idle reports whether the pod has a free concurrency slot.
func (p PodInfo) Idle() bool { return p.Ready && p.ActiveSteps < p.Slots }

// Runtime executes steps. Implementations must be safe for concurrent use.
//
// Architecture follows the reference implementation's worker model: pods are
// registered and heartbeat (mirroring its `workers` table), work is claimed
// from Postgres with SKIP LOCKED (mirroring PollTasks), and a pod that stops
// heartbeating has its work evicted (mirroring PollTasksOnInactiveWorkers).
//
// Contract for all implementations:
//
//   - Acquire returns ErrNoPodAvailable rather than blocking indefinitely, so
//     the orchestrator retains control over backpressure and can re-queue.
//   - Exec must respect ctx cancellation and Request.Timeout, and must return
//     within a bounded time after either fires, so the watchdog is never the
//     only thing preventing a hung step.
//   - Cleanup must fully reset the pod: kill stray processes, clear the work
//     directory. A step must not be able to affect the step that occupies the
//     pod next. This is the price of a warm pool over pod-per-step.
//   - Heartbeat answers "is this pod still worth waiting for", nothing more.
//     A live pod proves liveness, not progress; a step wedged in user code
//     will heartbeat forever. See RunnerPod.IsStale.
type Runtime interface {
	// Register adds a pod to the pool and begins heartbeating on its behalf.
	Register(ctx context.Context, pod domain.RunnerPod) error

	// Acquire reserves a concurrency slot on a healthy pod, waiting at most
	// until ctx or its own deadline.
	Acquire(ctx context.Context, deadline time.Time) (PodHandle, error)

	// Release returns a slot, performing cleanup first. If cleanup fails the
	// slot is not returned to circulation: a pod we could not scrub must not
	// be handed to the next step.
	Release(ctx context.Context, handle PodHandle, cleanErr error) error

	// Exec runs the step's command inside the pod. It must return
	// ExecResult even when the step failed; Err in the result signals that
	// the infrastructure failed, not the command.
	Exec(ctx context.Context, handle PodHandle, req Request) (ExecResult, error)

	// Heartbeat reports whether the pod is still alive. Called by the
	// watchdog sweep; a nil return within InactivityThreshold keeps the
	// pod's steps alive.
	Heartbeat(ctx context.Context, podID string) error

	// List returns the current pool, with liveness and capacity.
	List(ctx context.Context) ([]PodInfo, error)

	// Scale grows the pool toward targetIdle idle pods.
	Scale(ctx context.Context, targetIdle int) error

	// Close releases any client resources.
	Close() error
}

// Stringer makes ExecResult printable in test failures without importing fmt
// at every call site.
func (r ExecResult) String() string {
	return fmt.Sprintf("pod=%s exit=%d timedOut=%v err=%v duration=%s",
		r.Pod, r.ExitCode, r.TimedOut, r.Err, r.Duration)
}
