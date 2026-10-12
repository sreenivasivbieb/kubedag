# ADR 0004 — Results are fenced by attemptId

**Status:** accepted · **Date:** 2026-10-12

## Context

Because the step queue is at-least-once, a step can be dispatched more than
once. Suppose attempt 1 is dispatched and its pod dies; attempt 2 is dispatched
to a different pod and completes. Then attempt 1's pod — a partitioned network
partition rather than a dead node — finally returns a result.

## Decision

Every `StepResult` carries an `attemptId`, generated when the step is
dispatched. The orchestrator applies a result only when the attemptId matches
the step's current attempt. A mismatch is logged and discarded.

## Alternatives considered

**Rely on `isTerminal` alone (what Runexus does).** Rejected. It handles
"terminal then late result" correctly, and that is most of the risk. It does not
handle "attempt 1 still RUNNING when attempt 2 was dispatched", where both
results are non-terminal and the later one silently wins. That window exists
whenever a pod is evicted but not yet killed.

**Monotonic fencing tokens** (the approach taken in
[durable-execution-the-hard-way](https://github.com/hatchet-dev/durable-execution-the-hard-way),
fencing on `(task_id, retry_count, worker_id)`). Better than a bare counter
because it derives the fence from state already present rather than allocating
one. Not yet implemented here — Redis gives us no atomic compare-and-set
primitive across processes. `attemptId` is the pragmatic equivalent for v1 and
`ADR 0005` records the upgrade path.

## Consequences

**Good.** A late result from a superseded attempt cannot overwrite a newer
attempt's state.

**Good.** Cheap to check on the hot path, which matters because HEARTBEAT
messages are high-volume.

**Bad.** It fences *staleness*, not *wrongness*. A pod that legitimately holds
its attempt but whose command performed a side effect it never reported will
have that side effect repeated on retry. Only user-side idempotency, keyed on
the injected `ATTEMPT_ID`, can address that. **There is no exactly-once
execution here and we do not claim one.**
