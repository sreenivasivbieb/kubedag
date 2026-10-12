# ADR 0002 — Evict durable steps, retry everything else

**Status:** accepted · **Date:** 2026-10-12

## Context

A pod dies mid-execution. The step is `RUNNING`, with an event log recording
work already completed, and no process alive to finish it. Something must
decide what happens to that step.

## Decision

Classify steps as durable or non-durable at scheduling time, and branch on it:

- **Durable step → EVICT.** Return to `QUEUED` with `attempt` unchanged and
  `pod_id` cleared. The event log is preserved, so the resumed attempt replays
  completed work instead of redoing it.
- **Non-durable step → RETRY or FAIL** through normal failure handling,
  incrementing `attempt`.

This mirrors `lessons/07-durable-tasks/worker.go:208`:

```go
if task.IsDurable {
    // eviction: preserve event log, no retry_count bump
    _, err = w.queries.ReassignTask(ctx, tx, task.ID)
} else {
    // retry: same code path as a regular task failure
    err = w.queries.UpdateTaskFailed(ctx, tx, ...)
}
```

## Alternatives considered

**Always retry (uniform treatment).** Simpler — one code path. Rejected: it
throws away the event log on every pod loss, so a workflow of twenty steps
that dies on step nineteen restarts from step one. That defeats the entire
purpose of the event log.

**Always evict.** Rejected: an evicted step whose pod is gone for a
non-infrastructure reason (node drained, cluster autoscaled away) would loop
forever with no attempt budget. Eviction must still be bounded.

## Consequences

**Good.** Pod loss costs one step's worth of work, not the run's.

**Good.** The two paths reuse each other's machinery, so new failure modes
(lease expiry, pod deletion, node drain) need one handler, not three.

**Bad — this is the subtle part.** Eviction does not bump `attempt`, so an
evict loop is invisible to the retry budget and could spin indefinitely.
`internal/domain/events.go` marks a step durable explicitly so the distinction
is never inferred, and Phase 7's reconciliation must bound evictions
separately from retries. This is a design sharp edge, not a solved problem.

**Bad.** A pod that is merely unreachable (network partition) is
indistinguishable from a pod that is executing an infinite loop. We evict
optimistically and accept possible duplicate work, because the alternative —
waiting for certainty — means the run hangs. At-least-once, deliberately.