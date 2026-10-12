/**
 * DAG logic: validation, ready-step resolution, skip propagation.
 *
 * Every function here is pure. No Redis, no Kubernetes, no clock, no store.
 * That is the whole point — the part of an orchestrator most likely to harbour
 * a subtle bug (a cycle that slips through, a skip that skips too much, a
 * ready step that fires before its dependency) is exactly the part that must be
 * testable without a cluster and without sleeping.
 */

import type { StepStatus, Workflow, WorkflowState, WorkflowStep } from "../types/workflow";
import { isTerminal } from "../types/workflow";

export type Limits = {
  maxSteps: number;
  maxCommandBytes: number;
  maxStepIdLen: number;
};

/**
 * A validation failure carrying the HTTP status the API should return.
 *
 * Every failure is a 400: the client sent something structurally impossible,
 * and no amount of retrying will change that. Distinguishing 400 from 500 here
 * is what stops a malformed graph from being logged as a server fault.
 */
export class ValidationError extends Error {
  readonly status = 400;
  readonly field?: string;

  constructor(message: string, field?: string) {
    super(message);
    this.name = "ValidationError";
    this.field = field;
  }
}

/**
 * A step is ready when it is still PENDING and every dependency has
 * COMPLETED. Anything else fails.
 *
 * Only COMPLETED satisfies a dependency. RUNNING does not, FAILED does not,
 * and neither does SKIPPED. Treating "terminal" as "satisfied" would let a
 * failed branch unblock its dependents, which is precisely the bug
 * skipDependents exists to prevent.
 */
export function getReadySteps(
  steps: readonly WorkflowStep[],
  stepStatus: Readonly<Record<string, StepStatus>>,
): WorkflowStep[] {
  return steps.filter((step) => {
    if (stepStatus[step.id] !== "PENDING") return false;
    const deps = step.dependsOn;
    if (!deps || deps.length === 0) return true;
    return deps.every((d) => stepStatus[d] === "COMPLETED");
  });
}

/**
 * Marks every step transitively downstream of a failed step as SKIPPED.
 *
 * Breadth-first because the answer is "everything reachable", and BFS stops as
 * soon as it exhausts the frontier. Steps that are already terminal are not
 * re-marked: a step that completed before its dependency failed must keep its
 * real result rather than be overwritten with SKIPPED.
 */
export function skipDependents(state: WorkflowState, rootStepId: string): string[] {
  const skipped: string[] = [];
  const queue: string[] = [rootStepId];

  while (queue.length > 0) {
    const blockedId = queue.shift() as string;

    for (const step of state.steps) {
      if (!step.dependsOn?.includes(blockedId)) continue;

      const stepState = state.stepState[step.id];
      if (!stepState || isTerminal(stepState.status)) continue;

      stepState.status = "SKIPPED";
      stepState.error = `Skipped because dependency "${blockedId}" did not complete`;
      skipped.push(step.id);
      queue.push(step.id);
    }
  }

  return skipped;
}

/**
 * Steps that can never run: PENDING steps left behind once nothing more can
 * happen. Used by reconciliation to prove a run will terminate.
 */
export function findStrandedSteps(state: WorkflowState): string[] {
  return Object.values(state.stepState)
    .filter((s) => s.status === "PENDING")
    .map((s) => s.stepId);
}

/**
 * Validate a submitted workflow.
 *
 * Checks run cheapest-first and report the first violation precisely, because
 * a client fixing a 500-step graph wants "step "B" depends on unknown step "X""
 * rather than a generic rejection. Collecting every error at once is
 * tempting but unhelpful: the first error usually invalidates the rest.
 */
export function validateWorkflow(workflow: unknown, limits: Limits): Workflow {
  if (typeof workflow !== "object" || workflow === null) {
    throw new ValidationError("body must be a JSON object");
  }

  const w = workflow as Record<string, unknown>;

  if (typeof w.workflowId !== "string" || w.workflowId.length === 0) {
    throw new ValidationError("workflowId is required", "workflowId");
  }
  if (w.workflowId.length > limits.maxStepIdLen) {
    throw new ValidationError(
      `workflowId exceeds ${limits.maxStepIdLen} characters`,
      "workflowId",
    );
  }

  if (!Array.isArray(w.steps)) {
    throw new ValidationError("steps must be an array", "steps");
  }
  if (w.steps.length === 0) {
    throw new ValidationError("workflow must contain at least one step", "steps");
  }
  if (w.steps.length > limits.maxSteps) {
    throw new ValidationError(
      `workflow has ${w.steps.length} steps, exceeding the limit of ${limits.maxSteps}`,
      "steps",
    );
  }

  const stepIds = new Set<string>();

  w.steps.forEach((raw: unknown, index: number) => {
    if (typeof raw !== "object" || raw === null) {
      throw new ValidationError(`steps[${index}] must be an object`, `steps[${index}]`);
    }
    const step = raw as Record<string, unknown>;

    if (typeof step.id !== "string" || step.id.length === 0) {
      throw new ValidationError(`steps[${index}].id is required`, `steps[${index}].id`);
    }
    if (step.id.length > limits.maxStepIdLen) {
      throw new ValidationError(
        `steps[${index}].id exceeds ${limits.maxStepIdLen} characters`,
        `steps[${index}].id`,
      );
    }
    if (stepIds.has(step.id)) {
      throw new ValidationError(`duplicate step id "${step.id}"`, `steps[${index}].id`);
    }
    stepIds.add(step.id);

    if (typeof step.command !== "string" || step.command.length === 0) {
      throw new ValidationError(
        `step "${step.id}" requires a command`,
        `steps[${index}].command`,
      );
    }
    // Byte length, not .length: a multi-byte character costs more than one
    // byte, and the limit exists to bound memory, not characters.
    if (Buffer.byteLength(step.command, "utf8") > limits.maxCommandBytes) {
      throw new ValidationError(
        `step "${step.id}" command exceeds ${limits.maxCommandBytes} bytes`,
        `steps[${index}].command`,
      );
    }

    if (step.dependsOn !== undefined) {
      if (!Array.isArray(step.dependsOn)) {
        throw new ValidationError(
          `step "${step.id}".dependsOn must be an array`,
          `steps[${index}].dependsOn`,
        );
      }
      for (const dep of step.dependsOn) {
        if (typeof dep !== "string") {
          throw new ValidationError(
            `step "${step.id}".dependsOn entries must be strings`,
            `steps[${index}].dependsOn`,
          );
        }
      }
    }

    if (step.retries !== undefined) {
      if (typeof step.retries !== "number" || step.retries < 0 || !Number.isInteger(step.retries)) {
        throw new ValidationError(
          `step "${step.id}".retries must be a non-negative integer`,
          `steps[${index}].retries`,
        );
      }
    }
  });

  for (const raw of w.steps as Record<string, unknown>[]) {
    const id = raw.id as string;
    for (const dep of (raw.dependsOn as string[] | undefined) ?? []) {
      if (!stepIds.has(dep)) {
        throw new ValidationError(`step "${id}" depends on unknown step "${dep}"`, `${id}.dependsOn`);
      }
      if (dep === id) {
        throw new ValidationError(`step "${id}" depends on itself`, `${id}.dependsOn`);
      }
    }
  }

  const wf: Workflow = {
    workflowId: w.workflowId as string,
    steps: w.steps as WorkflowStep[],
    idempotencyKey: typeof w.idempotencyKey === "string" ? w.idempotencyKey : undefined,
  };

  assertAcyclic(wf.steps);
  return wf;
}

/**
 * Cycle detection by depth-first search with three-colour marking.
 *
 * The `onStack` set is what makes this correct rather than merely terminating.
 * A plain visited-set DFS reports "cycle" the moment it revisits a node, which
 * also fires on a diamond (A -> B -> D, A -> C -> D): D is visited twice
 * through different paths without any cycle existing. Three-colour marking
 * distinguishes "already finished, safe to skip" (visited) from "on the
 * current path, therefore a cycle" (onStack).
 */
export function assertAcyclic(steps: readonly WorkflowStep[]): void {
  const byId = new Map(steps.map((s) => [s.id, s]));
  const visited = new Set<string>();
  const onStack = new Set<string>();
  const path: string[] = [];

  const visit = (stepId: string): void => {
    if (onStack.has(stepId)) {
      // Report the actual cycle, not just the node that closed it. "A -> B ->
      // A" is actionable; "A" is not.
      const start = path.indexOf(stepId);
      const cycle = [...path.slice(start), stepId];
      throw new ValidationError(
        `workflow contains a cycle: ${cycle.join(" -> ")}`,
        `${stepId}.dependsOn`,
      );
    }
    if (visited.has(stepId)) return;

    onStack.add(stepId);
    path.push(stepId);

    for (const dep of byId.get(stepId)?.dependsOn ?? []) {
      visit(dep);
    }

    path.pop();
    onStack.delete(stepId);
    visited.add(stepId);
  };

  for (const step of steps) visit(step.id);
}

/**
 * Topological layers. Steps in the same layer have no dependency on each
 * other and may run concurrently; the i-th layer may only start once layer
 * i-1 is complete.
 *
 * Useful beyond visualisation: the maximum layer depth is the workflow's
 * critical-path length, which is the floor on wall-clock runtime.
 */
export function topoLayers(steps: readonly WorkflowStep[]): string[][] {
  assertAcyclic(steps);

  const remaining = new Map(steps.map((s) => [s.id, new Set(s.dependsOn ?? [])]));
  const layers: string[][] = [];

  while (remaining.size > 0) {
    const layer: string[] = [];

    for (const [id, deps] of remaining) {
      if ([...deps].every((d) => !remaining.has(d))) {
        layer.push(id);
      }
    }

    // A graph with a cycle would produce an empty layer; assertAcyclic above
    // rules that out, and this guard turns a future regression into a clear
    // error rather than a silent infinite loop.
    if (layer.length === 0) {
      throw new Error("topoLayers: cycle detected among " + [...remaining.keys()].join(", "));
    }

    for (const id of layer) remaining.delete(id);
    layers.push(layer);
  }

  return layers;
}