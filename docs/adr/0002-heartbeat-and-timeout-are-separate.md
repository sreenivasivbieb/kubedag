# ADR 0002 — Heartbeat and execution timeout are independent mechanisms

**Status:** accepted · **Date:** 2026-10-12

## Context

Runexus states:

> *"Missing pulses mean the pod died, not that the command is simply taking a
> long time."*

That is correct, but it is only half the picture, and the half that matters when
things go wrong.

## Decision

Two independent bounds on every running step:

| Mechanism | Bound | Detects |
|---|---|---|
| Heartbeat watchdog | `HEARTBEAT_INTERVAL_MS × HEARTBEAT_GRACE_BEATS` | pod gone — no events arriving at all |
| Execution timeout | `DEFAULT_STEP_TIMEOUT_MS` | command wedged — events arriving but work not finishing |

## Alternatives considered

**Heartbeat alone.** This is what Runexus does, and it has a hole. A command
like `sleep 100000`, or any infinite loop, inside a perfectly healthy pod
heartbeats forever. The watchdog never fires, the step never terminates, and the
run hangs indefinitely. Heartbeats prove the pod is alive; they say nothing
about whether the work is progressing.

**Timeout alone.** Rejected because it cannot distinguish "died" from "slow".
A step that legitimately takes 4 minutes under a 5-minute timeout looks
identical to one that is wedged.

## Consequences

**Good.** Each mechanism catches what the other structurally cannot. The
timeout catches a hung command on a live pod; the watchdog catches a dead pod
running a command that would never have timed out soon.

**Good.** Different verdicts. A timeout produces `TIMED_OUT`; a lost pod
produces `FAILED` with `retryable: true`. Users and retry policy need to tell
those apart.

**Bad.** `config.validate` must enforce `defaultStepTimeoutMs > livenessTimeoutMs`.
Otherwise every step times out before the watchdog could report a lost pod, and
the entire pod-death path becomes dead code that still appears to be tested.

**Bad — a real limit.** Heartbeat proves liveness, not progress. A command
making no forward progress while its pod is healthy is caught only by the
timeout, which is a blunt instrument: it cannot tell a 4-minute build from a
deadlock. True progress detection would need per-step instrumentation, which
means trusting the user's code to report progress. Out of scope for v1.
