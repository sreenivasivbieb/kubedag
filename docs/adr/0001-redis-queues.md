# ADR 0001 — Redis queues separate the control and data planes

**Status:** accepted · **Date:** 2026-10-12 · **Reference:** `mr-raj12/Runexus`

## Context

The orchestrator decides *what* to run. The pod manager decides *how* to run
it inside a container. These two have genuinely different failure modes: the
orchestrator does in-memory bookkeeping, while the pod manager deals with
`kubectl exec`, evicted pods and timeouts.

## Decision

The two communicate only through two Redis lists, drained with blocking
`BRPOP` so consumers park instead of busy-waiting:

- `step-queue` — orchestrator pushes ready steps; orchestrator drains and dispatches
- `result-queue` — pod manager pushes lifecycle events; orchestrator consumes

Neither component imports the other. The pod manager cannot read or write
workflow state, only report observations.

## Alternatives considered

**Direct function calls in one process.** Rejected. It is simpler and faster,
and it is also why Runexus, Temporal and most orchestrators are hard to scale
horizontally: the pod manager's event loop shares fate with the orchestrator's
bookkeeping. One blocked exec can stall the scheduler.

**Postgres as the broker (`SELECT ... FOR UPDATE SKIP LOCKED`).** Seriously
considered — it eliminates the dual-write window entirely, since "this step
exists" and "this step is claimed" become one atomic statement. Rejected for
now because it changes the process topology: the pod manager would need database
credentials and would become a state writer, which weakens the single-writer
invariant that makes the system comprehensible.

**Kafka / Streams.** Rejected for v1. Genuinely better at scale and gives real
consumer offsets, at the cost of a second datastore to operate and reason about.

## Consequences

**Good.** The single-writer invariant is structural, not a convention: the pod
manager has no store to write to.

**Good.** `BRPOP` parks the consumer, so an idle system costs almost nothing.

**Good.** The pod manager can be scaled or restarted independently.

**Bad — a real dual-write window.** `submitWorkflow` writes state and *then*
pushes to the step queue. A crash in between leaves a step `QUEUED` in the store
that no consumer will ever receive. Runexus handles this by persisting before
enqueueing (comment at `orchestrator.ts:36`), which narrows the window but does
not close it. Phase 7's reconciliation must re-drive stranded steps; this ADR
is the reason that routine exists.

**Bad.** Two moving parts to operate. If Redis is down, steps already running
keep running but nothing new starts, and results accumulate in a list nobody
is draining.

**Bad.** At-least-once delivery means duplicate results. A step may be
dispatched twice if a consumer dies between `BRPOP` and dispatch. The `isTerminal`
guard in `handleStepResult` absorbs most of this; `attemptId` fencing absorbs
the rest.
