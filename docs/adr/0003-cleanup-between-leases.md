# ADR 0003 — Pods are scrubbed between leases

**Status:** accepted · **Date:** 2026-10-12

## Context

Runexus leases pods from a pool and, in `PodPool.releasePod`, sets the lease
map entry back to `null`. That is the entire cleanup.

## Decision

Before a pod returns to circulation, reset it:

1. Kill every process in the pod except PID 1 and the running command.
2. Clear `/work` and `/tmp`.
3. Bound the whole operation by `CLEANUP_TIMEOUT_MS`.
4. If cleanup fails, **do not** return the pod to the pool. Report it and
   quarantine the pod.

## Alternatives considered

**Pod-per-step.** Rejected for the pool. A pod start costs seconds; the warm
pool exists to keep scheduling off the critical path. This is the main reason
the pool exists, and cleanup is the price of it.

**Accept the risk, matching Runexus.** Rejected. Once a step can write a file
or leave a background process, the next step to lease that pod inherits both.
That produces failures that look like nondeterminism in user code and are
extremely hard to diagnose. `cmd &` in a shell command is common enough that
this cannot be treated as an edge case.

## Consequences

**Good.** Steps are isolated from each other within a shared pod.

**Good.** Cleanup failure is surfaced rather than swallowed, so a wedged pod
becomes visible instead of silently poisoning every subsequent step.

**Bad.** Cleanup costs time on the hot path between steps. A pool of 7 pods
doing 2s steps spends a meaningful fraction of its capacity scrubbing. The
`CLEANUP_TIMEOUT_MS` bound keeps this from becoming an outage, but it is a real
throughput tax and Phase 11 must measure it.

**Bad — the hard case.** Killing stray processes reliably requires either tini
(as PID 1, which reaps zombies but does not kill orphans) or enumerating
processes inside the container. We do both. A process that has double-forked
itself fully detached can still survive; there is no bulletproof answer from
inside a container without cgroups or a purpose-built sandbox.

**Bad.** `readOnlyRootFilesystem` cannot be enabled while steps can write, so
the isolation story is weaker than a pod-per-step model would allow. A tmpfs
`/work` limits the blast radius to memory.
