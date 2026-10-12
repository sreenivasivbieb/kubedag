/**
 * The execution boundary: everything that puts user code into a container.
 *
 * This is the most important interface in the system. Above it (DAG maths,
 * state transitions, retry policy) is pure and unit-testable. Below it,
 * Kubernetes is slow, stateful, and fails in ways that are painful to
 * reproduce deliberately.
 *
 * The reference architecture talks to Kubernetes directly from PodPool. We keep
 * that behaviour in `src/k8s/` but route it through this interface, so that
 * failure injection in Phase 7 (delete a pod mid-exec, partition the network)
 * can be done against an in-memory fake with exact control over timing, instead
 * of by hoping kind cooperates.
 */

import type { Pod, PoolStatus } from "../types/workflow";

/** Sentinel the orchestrator recognises as backpressure, not a failure. */
export const NO_POD_AVAILABLE = "NO_POD_AVAILABLE";

export type ExecOutcome = {
  stdout: string;
  stderr: string;
  exitCode: number;
  /** True when we killed the command because it exceeded its budget. */
  timedOut: boolean;
  /** Set when infrastructure failed, as opposed to the command failing. */
  error?: string;
  /** True when `error` reflects infrastructure rather than user code. */
  retryable: boolean;
  durationMs: number;
  node?: string;
};

export type ExecRequest = {
  command: string;
  timeoutMs: number;
  /** Ceiling on captured output per stream. */
  maxLogBytes: number;
  /** Injected into the container environment so user code can deduplicate. */
  env: Record<string, string>;
  /** In-process context for the execution itself. */
  signal: AbortSignal;
};

export type Lease = {
  podId: string;
  podName: string;
  node?: string;
  leasedAt: number;
};

/**
 * Lease acquisition returns null rather than throwing for "nothing free", so
 * callers can distinguish backpressure (requeue and retry later) from a real
 * error (fail the step). Collapsing those two is how a saturated pool turns
 * into a run full of spurious failures.
 */
export type Runtime = {
  /** Discover the pool. Called once at boot and on demand after churn. */
  discover(): Promise<void>;

  /**
   * Reserve a pod, waiting no longer than `waitMs`.
   * Returns null if none became free in that window.
   */
  acquire(waitMs: number): Promise<Lease | null>;

  /**
   * Return a pod to the pool. MUST fully reset it first: kill stray processes
   * and clear the work directory.
   *
   * A pod that cannot be cleaned must not re-enter circulation, or one step
   * can silently corrupt the next. Implementations report cleanup failure to
   * the caller so the pod can be quarantined rather than recycled.
   */
  release(lease: Lease, cleanupTimeoutMs: number): Promise<{ clean: boolean; error?: string }>;

  /** Run a command in the leased pod, honouring timeoutMs and signal. */
  exec(lease: Lease, req: ExecRequest): Promise<ExecOutcome>;

  /** Force-kill a running command without waiting for it to exit. */
  kill(lease: Lease): Promise<void>;

  /** True when the pod answers a liveness probe. */
  isAlive(podId: string): Promise<boolean>;

  status(): Promise<PoolStatus>;
  pods(): Promise<Pod[]>;
};