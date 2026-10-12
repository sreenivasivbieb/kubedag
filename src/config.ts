/**
 * Configuration loaded from the environment.
 *
 * Every knob has a documented default so `bun run setup && bun start` works on
 * a laptop with zero configuration, while a deployment can override anything.
 *
 * Parsing is deliberately forgiving: a malformed numeric variable falls back
 * to its default rather than throwing. A typo in one env var should degrade a
 * system to a known-good configuration, not take the orchestrator down at boot.
 * Genuinely invalid *combinations* are still rejected in validate().
 */

function str(name: string, fallback: string): string {
  const v = process.env[name];
  return v === undefined || v === "" ? fallback : v;
}

function int(name: string, fallback: number): number {
  const v = process.env[name];
  if (v === undefined || v === "") return fallback;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) ? n : fallback;
}

function ms(name: string, fallback: number): number {
  const v = process.env[name];
  if (v === undefined || v === "") return fallback;
  const n = Number.parseInt(v, 10);
  return Number.isFinite(n) && n > 0 ? n : fallback;
}

export type Config = {
  port: number;

  redisHost: string;
  redisPort: number;
  /**
   * Namespaces all queue keys. Empty means `workflow:<pid>`, which isolates
   * concurrent dev sessions on one machine. Set it explicitly to share queues
   * between processes, which is what a multi-replica deployment needs.
   */
  redisQueuePrefix: string;

  kubeNamespace: string;
  kubePodLabel: string;
  runnerReplicas: number;

  /** How often a running step pulses. */
  heartbeatIntervalMs: number;
  /** Missed pulses tolerated before a step is declared lost. */
  heartbeatGraceBeats: number;
  /** Derived: heartbeatIntervalMs * heartbeatGraceBeats. */
  livenessTimeoutMs: number;
  /** How often the orchestrator scans for silent steps. */
  livenessScanIntervalMs: number;

  /**
   * Per-attempt execution timeout.
   *
   * Deliberately independent of the heartbeat timeout. A command wedged in an
   * infinite loop inside a perfectly healthy pod keeps heartbeating, so the
   * watchdog will never fire and only this bound can rescue the run.
   */
  defaultStepTimeoutMs: number;
  /** Ceiling on a client-requested timeout, so a submitted graph cannot pin a pod forever. */
  maxStepTimeoutMs: number;
  /** Budget for inter-lease cleanup. */
  cleanupTimeoutMs: number;
  /** Per-stream cap on captured output. */
  maxLogBytes: number;

  globalMaxConcurrentSteps: number;
  perRunMaxConcurrentSteps: number;

  /** Untrusted-input bounds. Enforced before any scheduling happens. */
  maxWorkflowBytes: number;
  maxStepsPerWorkflow: number;
  maxCommandBytes: number;
  maxStepIdLen: number;
};

export function loadConfig(): Config {
  const heartbeatIntervalMs = ms("HEARTBEAT_INTERVAL_MS", 10_000);
  const heartbeatGraceBeats = int("HEARTBEAT_GRACE_BEATS", 3);

  const cfg: Config = {
    port: int("PORT", 3000),

    redisHost: str("REDIS_HOST", "localhost"),
    redisPort: int("REDIS_PORT", 6379),
    redisQueuePrefix: str("REDIS_QUEUE_PREFIX", ""),

    kubeNamespace: str("KUBE_NAMESPACE", "kubedag-runner"),
    kubePodLabel: str("KUBE_POD_LABEL", "app=runner"),
    runnerReplicas: int("RUNNER_REPLICAS", 7),

    heartbeatIntervalMs,
    heartbeatGraceBeats,
    livenessTimeoutMs: heartbeatIntervalMs * heartbeatGraceBeats,
    livenessScanIntervalMs: ms("LIVENESS_SCAN_INTERVAL_MS", 5_000),

    defaultStepTimeoutMs: ms("DEFAULT_STEP_TIMEOUT_MS", 300_000),
    maxStepTimeoutMs: ms("MAX_STEP_TIMEOUT_MS", 1_800_000),
    cleanupTimeoutMs: ms("CLEANUP_TIMEOUT_MS", 15_000),
    maxLogBytes: int("MAX_LOG_BYTES", 65_536),

    globalMaxConcurrentSteps: int("GLOBAL_MAX_CONCURRENT_STEPS", 20),
    perRunMaxConcurrentSteps: int("PER_RUN_MAX_CONCURRENT_STEPS", 8),

    maxWorkflowBytes: int("MAX_WORKFLOW_BYTES", 262_144),
    maxStepsPerWorkflow: int("MAX_STEPS_PER_WORKFLOW", 500),
    maxCommandBytes: int("MAX_COMMAND_BYTES", 4_096),
    maxStepIdLen: int("MAX_STEP_ID_LEN", 128),
  };

  validate(cfg);
  return cfg;
}

function validate(cfg: Config): void {
  if (cfg.perRunMaxConcurrentSteps > cfg.globalMaxConcurrentSteps) {
    throw new Error(
      `PER_RUN_MAX_CONCURRENT_STEPS (${cfg.perRunMaxConcurrentSteps}) exceeds ` +
        `GLOBAL_MAX_CONCURRENT_STEPS (${cfg.globalMaxConcurrentSteps}); a run could ` +
        `never be schedulable`,
    );
  }

  // A zero/negative grace means one missed heartbeat fails the step, which is
  // indistinguishable from ordinary jitter. Anything below 2 makes transient
  // network delay fatal.
  if (cfg.heartbeatGraceBeats < 2) {
    throw new Error(
      `HEARTBEAT_GRACE_BEATS must be >= 2 (got ${cfg.heartbeatGraceBeats}); ` +
        `a single missed heartbeat is ordinary network jitter, not a dead pod`,
    );
  }

  // If the execution timeout is shorter than the liveness timeout, every step
  // times out before the watchdog could report a lost pod, making the whole
  // pod-death path dead code.
  if (cfg.defaultStepTimeoutMs <= cfg.livenessTimeoutMs) {
    throw new Error(
      `DEFAULT_STEP_TIMEOUT_MS (${cfg.defaultStepTimeoutMs}) must exceed the ` +
        `liveness timeout (${cfg.livenessTimeoutMs}) = HEARTBEAT_INTERVAL_MS * ` +
        `HEARTBEAT_GRACE_BEATS, otherwise pod death is never detected`,
    );
  }

  if (cfg.maxStepTimeoutMs < cfg.defaultStepTimeoutMs) {
    throw new Error(
      `MAX_STEP_TIMEOUT_MS (${cfg.maxStepTimeoutMs}) must be >= ` +
        `DEFAULT_STEP_TIMEOUT_MS (${cfg.defaultStepTimeoutMs})`,
    );
  }

  if (cfg.maxStepsPerWorkflow < 1) {
    throw new Error(`MAX_STEPS_PER_WORKFLOW must be >= 1 (got ${cfg.maxStepsPerWorkflow})`);
  }
}