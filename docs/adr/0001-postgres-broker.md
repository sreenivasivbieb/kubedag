# ADR 0001 — Postgres is the broker; no Redis

**Status:** accepted · **Date:** 2026-10-12

## Context

kubedag needs to hand work from the orchestrator (which decides *what* to run)
to pod managers (which *run* it). The original project brief specified Redis
lists for this. We are building on the architecture demonstrated in
[hatchet-dev/durable-execution-the-hard-way](https://github.com/hatchet-dev/durable-execution-the-hard-way),
whose stated opinion is "durable execution entirely in Postgres".

## Decision

Use Postgres as the broker, the state store, and the durable event log.
There is no Redis in the system.

Dequeue is a single statement:

```sql
WITH ready AS (
    SELECT id FROM steps
    WHERE run_id = $1 AND status = 'QUEUED'
    ORDER BY priority DESC, id ASC
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)
UPDATE steps s SET status = 'RUNNING', attempt = attempt + 1, pod_id = $3
FROM ready WHERE s.id = ready.id
RETURNING s.*;
```

## Alternatives considered

**Redis lists (original brief).** Rejected. It creates a dual-write problem: a
step's existence is recorded in Postgres while its wakeup signal lives in
Redis. A crash between the two loses the step permanently, and recovery
requires guessing what was in flight. The `SKIP LOCKED` dequeue makes the
queue *and* the record the same atomic operation.

**Redis Streams / Kafka.** Rejected for v1. Consumer groups give per-consumer
offsets and replay, which is genuinely attractive, but it adds a second
datastore to operate, back, and reason about for correctness. Revisit if
step volume outgrows what Postgres indexes comfortably.

**Postgres LISTEN/NOTIFY.** Not rejected — deferred. The reference repo lists
it as a future lesson. It removes the polling interval without adding a
dependency, so it composes with this decision rather than competing. Worth
doing once measurement (Phase 11) shows polling latency is material.

## Consequences

**Good.** One transactional boundary covers "this step exists" and "this step
is ready to be claimed". Crash recovery is a `SELECT` over non-terminal runs
rather than a reconstruction from queue semantics. Fewer moving parts to run
locally, fewer failure modes in the integration tests.

**Bad — stated plainly.** Polling. Step pickup latency is bounded below by the
poll interval, where a queue would deliver immediately. `LISTEN`/`NOTIFY`
mitigates but does not eliminate this; it is wakeup-on-change, not
push-with-delivery-guarantee, so we still need `SKIP LOCKED` for correctness.

**Bad.** Every orchestrator polls the same hot table. At high step volume this
contends with the writes it is waiting for. Mitigated by the
`(run_id, status, priority, id)` index, but it is a real ceiling and Phase 11
must measure it rather than assume it is fine.

**Bad.** Claim latency competes with claim throughput. A long-running step does
not hold its row lock (the dequeue statement commits immediately), so this is
less severe than it first appears.