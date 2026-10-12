/**
 * Shared vocabulary for the whole system.
 *
 * Kept in one file because these types cross every boundary: HTTP payloads,
 * Redis messages, Kubernetes exec results, and API responses. Splitting them
 * per-layer would let the wire format drift from the internal one, which is
 * how "the queue says one thing and the store says another" bugs happen.
 */

// --- Step lifecycle ---------------------------------------------------------

/**
 * Step states.
 *
 * PENDING   declared, dependencies not yet satisfied
 * QUEUED    dependencies satisfied, waiting for a pod
 * RUNNING   leased to a pod, command in flight
 * COMPLETED terminal, exit code 0
 * FAILED    terminal, non-zero exit, timeout, lost pod, or retry budget spent
 * SKIPPED   terminal, an upstream dependency failed
 * TIMED_OUT terminal, execution timeout elapsed
 * CANCELLED terminal, run was cancelled
 */
export type StepStatus =
  | "PENDING"
  | "QUEUED"
  | "RUNNING"
  | "COMPLETED"
  | "FAILED"
  | "SKIPPED"
  | "TIMED_OUT"
  | "CANCELLED";

/**
 * States a step can never leave.
 *
 * TIMED_OUT is terminal in its own right rather than a flavour of FAILED:
 * the user needs to distinguish "your program exited non-zero" from "we killed
 * it for running too long", and the retry policy differs.
 */
const TERMINAL_STATUSES: ReadonlySet<StepStatus> = new Set<StepStatus>([
  "COMPLETED",
  "FAILED",
  "SKIPPED",
  "TIMED_OUT",
  "CANCELLED",
]);

export function isTerminal(status: StepStatus): boolean {
  return TERMINAL_STATUSES.has(status);
}

/** Run-level status. The run is failed if any step failed or was skipped. */
export type WorkflowStatus = "pending" | "running" | "completed" | "failed" | "cancelled";

const TERMINAL_WORKFLOW_STATUSES: ReadonlySet<WorkflowStatus> = new Set<WorkflowStatus>([
  "completed",
  "failed",
  "cancelled",
]);

export function isWorkflowTerminal(status: WorkflowStatus): boolean {
  return TERMINAL_WORKFLOW_STATUSES.has(status);
}

// --- Authoring surface ------------------------------------------------------

export type ResourceSpec = {
  cpu?: string;
  memory?: string;
};

/**
 * One node in the DAG, as submitted by a client.
 *
 * `command` is a shell string by design: the brief specifies shell commands,
 * and every shell metacharacter is therefore meaningful. It is executed via
 * `sh -c` INSIDE the runner pod, never interpolated into anything the
 * orchestrator itself evaluates. See docs/adr/0004-command-execution.md.
 */
export type WorkflowStep = {
  id: string;
  command: string;
  image?: string;
  dependsOn?: string[];
  env?: Record<string, string>;
  resources?: ResourceSpec;
  /** Attempts beyond the first. 0 means a single attempt. */
  retries?: number;
  /** Per-step override of DEFAULT_STEP_TIMEOUT_MS. */
  timeoutMs?: number;
  /**
   * When true, this step's failure does not skip its dependents. The run still
   * fails, but branches downstream of a tolerable step continue.
   */
  continueOnFailure?: boolean;
};

export type Workflow = {
  workflowId: string;
  steps: WorkflowStep[];
  /** Optional client-supplied key making submission idempotent. */
  idempotencyKey?: string;
};

// --- Persisted state --------------------------------------------------------

/**
 * Mutable state for one step within a run.
 *
 * attempt and attemptId are the fencing mechanism. A result carrying a stale
 * attemptId is rejected rather than applied, which stops a slow pod from
 * overwriting the outcome of a newer attempt. See docs/adr/0005-fencing.md.
 */
export type StepState = {
  stepId: string;
  status: StepStatus;
  podId: string | null;
  stdout?: string;
  stderr?: string;
  exitCode?: number;
  error?: string;
  startedAt?: number;
  endedAt?: number;
  lastHeartbeatAt?: number;
  /** Monotonic per-step attempt counter. 0 before the first attempt. */
  attempt: number;
  /** Unique id for the current attempt. Exposed to user code for dedup. */
  attemptId?: string;
  /** Set when the pod was evicted rather than the command failing. */
  evictedAt?: number;
  /** True while the step has an event-log entry and can be replayed. */
  durable: boolean;
  /** Bounded key/value outputs consumed by downstream steps. */
  outputs?: Record<string, string>;
};

export type WorkflowState = {
  workflowId: string;
  status: WorkflowStatus;
  /** Immutable author intent, retained after submission. */
  steps: WorkflowStep[];
  stepState: Record<string, StepState>;
  idempotencyKey?: string;
  submittedAt: number;
  startedAt?: number;
  endedAt?: number;
};

// --- Queue messages ---------------------------------------------------------

/** A step awaiting a runner pod. */
export type QueuedStep = {
  workflowId: string;
  stepId: string;
  command: string;
  attempt: number;
  attemptId: string;
  timeoutMs: number;
  enqueuedAt: number;
};

/**
 * Lifecycle events the pod manager reports back.
 *
 * Every message carries attemptId so the orchestrator can discard results from
 * a superseded attempt. HEARTBEAT carries no payload beyond that identity for
 * exactly this reason: it is high-volume and must stay cheap to compare.
 */
export type StepResultStatus =
  | "RUNNING"
  | "HEARTBEAT"
  | "COMPLETED"
  | "FAILED"
  | "TIMED_OUT"
  | "CANCELLED";

export type StepResult = {
  workflowId: string;
  stepId: string;
  attemptId: string;
  podId: string;
  status: StepResultStatus;
  at: number;
  stdout?: string;
  stderr?: string;
  exitCode?: number;
  error?: string;
  durationMs?: number;
  node?: string;
  /** True when the failure was infrastructure, not user code. */
  retryable?: boolean;
};

// --- Durable event log ------------------------------------------------------

export type EventKind = "STEP_STARTED" | "STEP_COMPLETED";

/**
 * Append-only record of what a step did.
 *
 * Present so a resumed attempt can replay completed work instead of repeating
 * side effects. The event log, not the pod, is the source of truth for
 * progress; see docs/adr/0006-event-log.md.
 */
export type StepEvent = {
  workflowId: string;
  stepId: string;
  eventId: number;
  kind: EventKind;
  /** Fingerprint of the inputs, used to detect non-determinism on replay. */
  key: string;
  createdAt: number;
  outputs?: Record<string, string>;
};

// --- Cluster ----------------------------------------------------------------

export type Pod = {
  podId: string;
  podName: string;
  namespace: string;
  node?: string;
  ready: boolean;
  phase: string;
};

export type PoolStatus = {
  total: number;
  available: number;
  leased: string[];
};