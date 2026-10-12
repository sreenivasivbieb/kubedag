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
	// ErrLeaseHeld means another controller holds the lease on a pod.
	ErrLeaseHeld = errors.New("runtime: lease held by another owner")
	// ErrLeaseExpired means the lease existed but its TTL lapsed before use.
	ErrLeaseExpired = errors.New("runtime: lease expired")
	// ErrNoPodAvailable means the pool is empty and cannot scale fast enough
	// to satisfy the request before its own deadline.
	ErrNoPodAvailable = errors.New("runtime: no pod available")
)

// Lease is exclusive, time-bounded ownership of a single runner pod.
//
// Fencing is the point. If the orchestrator crashes after acquiring a lease,
// that lease must not stay held forever: it expires on its own and another
// orchestrator can take the pod. The FencingToken increases on every
// acquisition, so a result produced under an older token can be detected as
// stale and discarded (see Phase 6).
type Lease struct {
	Pod          string        `json:"pod"`
	LeaseID      string        `json:"leaseId"`
	FencingToken int64         `json:"fencingToken"`
	AcquiredAt   time.Time     `json:"acquiredAt"`
	TTL          time.Duration `json:"ttl"`
}

// Valid reports whether the lease is still within its TTL.
func (l Lease) Valid(now time.Time) bool {
	return now.Sub(l.AcquiredAt) < l.TTL
}

// ExecResult is the outcome of one attempt as observed by the runtime.
//
// Note what is *absent*: there is no field for "is this result still
// relevant". Freshness is decided by the orchestrator comparing FencingToken,
// not by the pod. A pod that was evicted and came back must not be able to
// resurrect a stale result by virtue of being alive.
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
	// AttemptID and FencingToken are injected into the container environment
	// so user code can deduplicate its own side effects (at-least-once
	// delivery is all we can offer; idempotency is the user's job, and we
	// make it possible).
	AttemptID    string
	FencingToken int64
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
	// LeaseOwner and LeaseExpiry reflect the control-plane lease, which is
	// authoritative over pod state. A pod may be Running while unleased.
	LeaseOwner  string    `json:"leaseOwner,omitempty"`
	LeaseExpiry time.Time `json:"leaseExpiry,omitempty"`
	Leased      bool      `json:"leased"`
}

// Runtime executes steps. Implementations must be safe for concurrent use.
//
// Contract for all implementations:
//
//   - Acquire returns ErrNoPodAvailable rather than blocking indefinitely, so
//     the orchestrator retains control over backpressure and can re-queue.
//   - Exec must respect ctx cancellation and Request.Timeout, and must return
//     within a bounded time after either fires, so the watchdog is never the
//     only thing preventing a hung step.
//   - Cleanup must fully reset the pod: kill stray processes, clear the work
//     directory. A step must not be able to affect the step that leases the
//     pod next. This is the price of a warm pool over pod-per-step.
//   - Heartbeat answers "is this pod still worth waiting for", nothing more.
//     A live pod proves liveness, not progress; see the watchdog design.
type Runtime interface {
	// Acquire leases an idle pod, waiting at most until ctx or deadline.
	Acquire(ctx context.Context, owner string, ttl time.Duration) (Lease, error)

	// Renew extends an existing lease. Returns ErrLeaseExpired if the lease
	// was already lost, which tells the orchestrator its work is orphaned.
	Renew(ctx context.Context, lease Lease, ttl time.Duration) (Lease, error)

	// Release returns a pod to the pool. Cleanup is performed before the pod
	// is marked idle, so a failed Cleanup keeps the pod out of circulation
	// rather than poisoning the next tenant.
	Release(ctx context.Context, lease Lease, cleanErr error) error

	// Exec runs the step's command inside the leased pod. It must return
	// ExecResult even when the step failed; Err in the result signals that
	// the infrastructure failed, not the command.
	Exec(ctx context.Context, lease Lease, req Request) (ExecResult, error)

	// Heartbeat reports whether the pod backing a lease is still alive.
	Heartbeat(ctx context.Context, lease Lease) error

	// List returns the current pool, leased and idle.
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
