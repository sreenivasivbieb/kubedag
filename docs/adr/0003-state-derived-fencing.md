# ADR 0003 — Fence on state-derived columns, not a monotonic token

**Status:** accepted · **Date:** 2026-10-12

## Context

After a pod is evicted and its step re-attempted elsewhere, the old pod may
still be alive (network partition) and may eventually try to report a result
or append to the event log. Something must reject that late write.

## Decision

Fence writes on the composite that already exists in the row:

```sql
SELECT * FROM steps
WHERE run_id = $1 AND step_id = $2
  AND attempt = $3          -- we are the current attempt
  AND pod_id = $4           -- and we are the pod holding it
  AND status = 'RUNNING'
FOR UPDATE SKIP LOCKED;
```

Zero rows returned means the fence was lost and the caller must abandon its
write. Adapted from the reference repo's `AcquireDurableTaskEventLog`
(`lessons/07-durable-tasks/sql/queries.sql`), which fences on
`(task_id, retry_count, worker_id)` for the same reason.

## Alternatives considered

**A monotonic fencing token counter** (my original Phase 0 design). Rejected.
It requires an allocator that must stay consistent under crashes, and a token
that increments but never resets — leaking state for every step ever created.
Deriving the fence from `attempt` and `pod_id` gets the same guarantee from
columns that already exist and are already written transactionally.

**Trust the pod's self-report.** Rejected: the whole failure mode is a pod
whose view of reality is stale.

**Leases with TTL (original Phase 0).** Rejected as the *primary* mechanism.
Leases are time-based, so they cannot distinguish "slow but alive" from "dead"
and force a choice between too-aggressive eviction (duplicate work) and
too-late eviction (hung runs). Heartbeats carry actual liveness evidence;
leases remain useful only as a secondary GC mechanism in Phase 7.

## Consequences

**Good.** Fencing needs no extra allocation, no counter to persist, and works
across orchestrator restarts because it is evaluated entirely in the database.

**Good.** `SKIP LOCKED` on the acquiring query means a fenced-off caller
returns immediately instead of blocking behind the current holder.

**Bad.** `attempt` and `pod_id` must be updated in the *same transaction* that
claims the step. Splitting them reintroduces a window where two pods both
believe they hold the fence.

**Bad — a real gap.** The fence rejects *stale* writes, not *wrong* ones. A pod
that legitimately holds the fence but whose command had a side effect it never
reported will have that side effect repeated on retry. Only user-side
idempotency, via the injected `ATTEMPT_ID`, can address this. There is no
exactly-once here and we do not claim one.