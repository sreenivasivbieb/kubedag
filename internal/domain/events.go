package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Errors mirroring the reference implementation's durable event log
// (hatchet-dev/durable-execution-the-hard-way, lesson 05-06).
var (
	// ErrEventLogNotAcquired means the caller no longer holds the fence and
	// must not write to the event log. The canonical cause is eviction: the
	// pod went silent and the step was reassigned to another pod.
	ErrEventLogNotAcquired = errors.New("step event log lock not acquired; step may have been evicted")

	// ErrNonDeterminism means this attempt tried to replay an event log
	// position with different content than what was recorded. Either the
	// DAG changed, or control flow now diverges on a retry. Both are user
	// errors and are surfaced rather than silently tolerated.
	ErrNonDeterminism = errors.New("non-determinism detected in step event log")
)

// EventKind enumerates entries in a step's append-only event log.
//
// Only STEP_STARTED exists in v1, mirroring the reference implementation's
// TASK_STARTED. It is deliberately not an open string: adding a kind means
// teaching the replay logic what that event means, and a typo would otherwise
// become a silent no-op.
type EventKind string

const (
	// EventStepStarted records "this step was invoked with these inputs".
	// Replay consults this to decide whether to skip the step.
	EventStepStarted EventKind = "STEP_STARTED"
)

// StepEvent is one entry in a step's append-only event log.
//
// Append-only is the whole trick. On resume, the engine walks the log in
// EventID order and replays results instead of re-executing, so a crash costs
// a retry rather than duplicated side effects.
//
// Key is a fingerprint of (Kind, StepSpec identity, inputs). Comparing it on
// replay is how we detect non-determinism — see EventKey.
type StepEvent struct {
	RunID   string    `json:"runId"`
	StepID  string    `json:"stepId"`
	EventID int64     `json:"eventId"`
	Kind    EventKind `json:"kind"`
	Key     string    `json:"key"`
	// Outputs are the small key/value results a SUCCEEDED step produces,
	// consumed by downstream steps. Bounded in size by MaxOutputsBytes.
	Outputs     map[string]string `json:"outputs,omitempty"`
	ChildStepID *int64            `json:"childStepId,omitempty"`
	CreatedAt   time.Time         `json:"createdAt"`
}

// EventKey fingerprints an event for determinism checking.
//
// Adapted from the reference implementation's createKey. The 0x00 separator
// prevents collisions between adjacent fields: without it, kind="AB" +
// name="C" and kind="A" + name="BC" would hash identically.
//
// The fields hashed here are everything that decides *what code runs*.
// Deliberately excluded: attempt number, timestamps, pod name, fencing token.
// Those legitimately differ between replays, and including them would make
// every resume look like non-determinism.
func EventKey(kind EventKind, stepID string, inputDigest []byte) string {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0x00})
	h.Write([]byte(stepID))
	h.Write([]byte{0x00})
	if len(inputDigest) > 0 {
		h.Write(inputDigest)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Fence identifies the single legitimate writer of a step's event log at a
// given moment.
//
// This is the reference implementation's AcquireDurableTaskEventLog predicate
// (lessons/07-durable-tasks/sql/queries.sql), which fences on
// (task_id, retry_count, worker_id). We keep the same shape: a write is legal
// only if the caller is still the current attempt on a still-live pod.
//
// The property that matters: a pod that was evicted, whose step was reassigned
// and re-attempted elsewhere, fails this check. It cannot append to the event
// log and cannot report a result. No counter needs maintaining — the fence is
// derived entirely from state the database already holds.
type Fence struct {
	RunID    string    `json:"runId"`
	StepID   string    `json:"stepId"`
	Attempt  int       `json:"attempt"`
	PodID    string    `json:"podId"`
	Acquired time.Time `json:"acquired"`
}

// Matches reports whether a fence still identifies the current holder.
// Implemented in the store as the WHERE clause of the acquiring query; this
// method exists so call sites and tests can express the same predicate.
func (f Fence) Matches(attempt int, podID string) bool {
	return f.Attempt == attempt && f.PodID == podID
}

// RunnerPod is a registered member of the execution pool, mirroring the
// reference implementation's `workers` table.
//
// Registration plus heartbeat is what lets us detect a pod that died without
// getting the chance to report anything. Without it, a step whose pod was
// evicted mid-execution sits in RUNNING forever and the run never terminates.
type RunnerPod struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Node string `json:"node"`
	// LastHeartbeatAt is written by the pod's own heartbeat loop. Staleness
	// beyond InactivityThreshold is the eviction signal.
	LastHeartbeatAt time.Time `json:"lastHeartbeatAt"`
	// Slots is this pod's declared concurrency. One pod can host several
	// concurrent execs up to its slot budget.
	Slots int `json:"slots"`
	// ActiveSteps is the number of steps currently leased to this pod.
	ActiveSteps int `json:"activeSteps"`
}

// IsStale reports whether the pod has missed heartbeats for longer than the
// configured threshold.
//
// Honest limitation: a heartbeat proves only that the pod's agent is still
// running and talking to us. It does NOT prove the step's command is making
// progress. A step stuck in an uninterruptible sleep, or deadlocked in user
// code, will happily heartbeat forever. That is why the per-step timeout and
// the heartbeat watchdog are two independent mechanisms, not one.
func (p RunnerPod) IsStale(now time.Time, threshold time.Duration) bool {
	return now.Sub(p.LastHeartbeatAt) > threshold
}

// EvictionAction is what should happen to a step found on a stale pod.
//
// This mirrors the single most instructive branch in the reference
// implementation (lessons/07-durable-tasks/worker.go:208): a durable step is
// EVICTED, not retried. Eviction returns it to the queue with its event log
// intact, so it resumes rather than restarts. A non-durable step is RETRIED,
// because there is no log to preserve.
type EvictionAction string

const (
	// Evict returns the step to QUEUED with attempt unchanged, preserving the
	// event log. This is the durable path.
	Evict EvictionAction = "EVICT"
	// Retry routes the step through normal failure handling, incrementing the
	// attempt counter. Used for non-durable work and for steps whose
	// ContinueOnFailure semantics apply.
	Retry EvictionAction = "RETRY"
	// Fail terminally fails the step. Used when the attempt budget is
	// exhausted or the failure is classified non-retryable.
	Fail EvictionAction = "FAIL"
)
