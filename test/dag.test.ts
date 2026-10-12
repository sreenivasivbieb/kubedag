import { describe, expect, test } from "bun:test";

import {
  ValidationError,
  assertAcyclic,
  getReadySteps,
  skipDependents,
  topoLayers,
  validateWorkflow,
} from "../src/backend/dag";
import type { StepStatus, WorkflowState, WorkflowStep } from "../src/types/workflow";

const limits = { maxSteps: 500, maxCommandBytes: 4096, maxStepIdLen: 128 };

function wf(workflowId: string, steps: WorkflowStep[]) {
  return { workflowId, steps };
}

function state(steps: WorkflowStep[], statuses: Record<string, StepStatus>): WorkflowState {
  const stepState: WorkflowState["stepState"] = {};
  for (const s of steps) {
    stepState[s.id] = {
      stepId: s.id,
      status: statuses[s.id] ?? "PENDING",
      podId: null,
      attempt: 0,
      durable: false,
    };
  }
  return {
    workflowId: "wf",
    status: "running",
    steps,
    stepState,
    submittedAt: 0,
  };
}

describe("getReadySteps", () => {
  const steps: WorkflowStep[] = [
    { id: "A", command: "echo a" },
    { id: "B", command: "echo b", dependsOn: ["A"] },
    { id: "C", command: "echo c", dependsOn: ["A"] },
    { id: "D", command: "echo d", dependsOn: ["B", "C"] },
  ];

  test("only dependency-free steps are ready initially", () => {
    const ready = getReadySteps(steps, {
      A: "PENDING",
      B: "PENDING",
      C: "PENDING",
      D: "PENDING",
    });
    expect(ready.map((s) => s.id)).toEqual(["A"]);
  });

  test("both branches become ready in parallel once A completes", () => {
    const ready = getReadySteps(steps, {
      A: "COMPLETED",
      B: "PENDING",
      C: "PENDING",
      D: "PENDING",
    });
    // Order is declaration order, which is deterministic and therefore
    // testable. Parallelism comes from the dispatch loop, not from this order.
    expect(ready.map((s) => s.id)).toEqual(["B", "C"]);
  });

  test("a diamond stays blocked until every branch completes", () => {
    const ready = getReadySteps(steps, {
      A: "COMPLETED",
      B: "COMPLETED",
      C: "RUNNING",
      D: "PENDING",
    });
    expect(ready.map((s) => s.id)).toEqual([]);
  });

  test("the join step is ready once both branches complete", () => {
    const ready = getReadySteps(steps, {
      A: "COMPLETED",
      B: "COMPLETED",
      C: "COMPLETED",
      D: "PENDING",
    });
    expect(ready.map((s) => s.id)).toEqual(["D"]);
  });

  // This is the test that matters most in the whole file. Only COMPLETED
  // satisfies a dependency. Treating "terminal" as "satisfied" would unblock
  // dependents of a FAILED step, which is exactly what skipDependents exists
  // to prevent.
  test("FAILED does not satisfy a dependency", () => {
    const ready = getReadySteps(steps, {
      A: "FAILED",
      B: "PENDING",
      C: "PENDING",
      D: "PENDING",
    });
    expect(ready).toEqual([]);
  });

  test("RUNNING does not satisfy a dependency", () => {
    const ready = getReadySteps(steps, {
      A: "RUNNING",
      B: "PENDING",
      C: "PENDING",
      D: "PENDING",
    });
    expect(ready).toEqual([]);
  });

  test("SKIPPED does not satisfy a dependency", () => {
    const ready = getReadySteps(steps, {
      A: "SKIPPED",
      B: "PENDING",
      C: "PENDING",
      D: "PENDING",
    });
    expect(ready).toEqual([]);
  });

  test("an already-queued step is not re-returned", () => {
    const ready = getReadySteps([{ id: "A", command: "x" }], { A: "QUEUED" });
    expect(ready).toEqual([]);
  });
});

describe("assertAcyclic", () => {
  // A naive visited-set DFS reports a false cycle here: D is reached twice by
  // two different paths, but no cycle exists. The three-colour marking in
  // assertAcyclic distinguishes "finished" from "on the current path", so this
  // must pass.
  test("a diamond is NOT a cycle", () => {
    expect(() =>
      assertAcyclic([
        { id: "A", command: "x" },
        { id: "B", command: "x", dependsOn: ["A"] },
        { id: "C", command: "x", dependsOn: ["A"] },
        { id: "D", command: "x", dependsOn: ["B", "C"] },
      ]),
    ).not.toThrow();
  });

  test("detects a two-step cycle", () => {
    expect(() =>
      assertAcyclic([
        { id: "A", command: "x", dependsOn: ["B"] },
        { id: "B", command: "x", dependsOn: ["A"] },
      ]),
    ).toThrow(/cycle/i);
  });

  test("reports the actual path, not just the closing node", () => {
    let message = "";
    try {
      assertAcyclic([
        { id: "A", command: "x", dependsOn: ["C"] },
        { id: "B", command: "x", dependsOn: ["A"] },
        { id: "C", command: "x", dependsOn: ["B"] },
      ]);
    } catch (e) {
      message = (e as Error).message;
    }
    // "A" alone is unactionable; the client needs the whole path. Note the
    // direction: the path follows *dependency* edges, so it reads backwards
    // through execution order ("A waits for C"). All four nodes appear, and
    // the first is repeated at the end to close the loop.
    expect(message).toContain("->");
    const nodes = message.replace(/^workflow contains a cycle: /, "").split(" -> ");
    expect(nodes).toHaveLength(4);
    expect(nodes[0]).toBe(nodes[3]);
    expect(new Set(nodes).size).toBe(3);
  });

  test("detects a self-loop", () => {
    expect(() =>
      assertAcyclic([{ id: "A", command: "x", dependsOn: ["A"] }]),
    ).toThrow(/cycle/i);
  });

  test("detects a long cycle that does not include the first step", () => {
    expect(() =>
      assertAcyclic([
        { id: "A", command: "x" },
        { id: "B", command: "x", dependsOn: ["C"] },
        { id: "C", command: "x", dependsOn: ["D"] },
        { id: "D", command: "x", dependsOn: ["B"] },
      ]),
    ).toThrow(/cycle/i);
  });

  test("a long DAG is acyclic", () => {
    const steps: WorkflowStep[] = Array.from({ length: 200 }, (_, i) => ({
      id: `s${i}`,
      command: "x",
      dependsOn: i === 0 ? [] : [`s${i - 1}`],
    }));
    expect(() => assertAcyclic(steps)).not.toThrow();
  });
});

describe("validateWorkflow", () => {
  test("accepts a minimal valid workflow", () => {
    const out = validateWorkflow(wf("wf-1", [{ id: "A", command: "echo hi" }]), limits);
    expect(out.workflowId).toBe("wf-1");
    expect(out.steps).toHaveLength(1);
  });

  test("rejects a non-object body", () => {
    expect(() => validateWorkflow("nope", limits)).toThrow(ValidationError);
    expect(() => validateWorkflow(null, limits)).toThrow(/JSON object/);
  });

  test("rejects a missing workflowId", () => {
    expect(() => validateWorkflow({ steps: [] }, limits)).toThrow(/workflowId is required/);
  });

  test("rejects an empty steps array", () => {
    expect(() => validateWorkflow(wf("wf", []), limits)).toThrow(/at least one step/);
  });

  test("rejects duplicate step ids", () => {
    expect(() =>
      validateWorkflow(
        wf("wf", [
          { id: "A", command: "x" },
          { id: "A", command: "y" },
        ]),
        limits,
      ),
    ).toThrow(/duplicate step id "A"/);
  });

  test("rejects a dangling dependency", () => {
    expect(() =>
      validateWorkflow(wf("wf", [{ id: "A", command: "x", dependsOn: ["ghost"] }]), limits),
    ).toThrow(/depends on unknown step "ghost"/);
  });

  test("rejects self-dependency", () => {
    expect(() =>
      validateWorkflow(wf("wf", [{ id: "A", command: "x", dependsOn: ["A"] }]), limits),
    ).toThrow(/depends on itself/);
  });

  test("rejects a missing command", () => {
    expect(() => validateWorkflow(wf("wf", [{ id: "A" } as WorkflowStep]), limits)).toThrow(
      /requires a command/,
    );
  });

  test("enforces the step-count limit", () => {
    const steps = Array.from({ length: 11 }, (_, i) => ({ id: `s${i}`, command: "x" }));
    expect(() => validateWorkflow(wf("wf", steps), { ...limits, maxSteps: 10 })).toThrow(
      /exceeding the limit of 10/,
    );
  });

  // The bound exists to cap memory, so it must be measured in bytes. A
  // multi-byte character costs more than one .length unit, and a limit that
  // undercounts lets an oversized command through.
  test("command byte limit counts bytes, not characters", () => {
    const emojiCommand = "echo " + "😀".repeat(100);
    // 205 UTF-16 units but 405 bytes. A limit written against .length would
    // admit ~2x the intended memory, which is exactly the kind of bound that
    // looks correct in review and is wrong in production.
    expect(emojiCommand.length).toBe(205);
    expect(Buffer.byteLength(emojiCommand, "utf8")).toBe(405);

    expect(() =>
      validateWorkflow(wf("wf", [{ id: "A", command: emojiCommand }]), {
        ...limits,
        maxCommandBytes: 300,
      }),
    ).toThrow(/exceeds 300 bytes/);

    // Under a character-based limit of 300 this would have been admitted.
    expect(() =>
      validateWorkflow(wf("wf", [{ id: "A", command: emojiCommand }]), {
        ...limits,
        maxCommandBytes: 400,
      }),
    ).toThrow(/exceeds 400 bytes/);
  });

  test("enforces the step-id length limit", () => {
    expect(() =>
      validateWorkflow(wf("wf", [{ id: "x".repeat(200), command: "y" }]), limits),
    ).toThrow(/exceeds 128 characters/);
  });

  test("rejects negative retries", () => {
    expect(() =>
      validateWorkflow(wf("wf", [{ id: "A", command: "x", retries: -1 }]), limits),
    ).toThrow(/non-negative integer/);
  });

  test("rejects non-integer retries", () => {
    expect(() =>
      validateWorkflow(wf("wf", [{ id: "A", command: "x", retries: 1.5 }]), limits),
    ).toThrow(/non-negative integer/);
  });

  test("rejects a malformed dependsOn", () => {
    expect(() =>
      validateWorkflow(
        wf("wf", [{ id: "A", command: "x", dependsOn: "B" as unknown as string[] }]),
        limits,
      ),
    ).toThrow(/dependsOn must be an array/);
  });

  test("rejects a cycle with a 400-shaped error", () => {
    try {
      validateWorkflow(
        wf("wf", [
          { id: "A", command: "x", dependsOn: ["B"] },
          { id: "B", command: "x", dependsOn: ["A"] },
        ]),
        limits,
      );
      throw new Error("expected a throw");
    } catch (e) {
      expect(e).toBeInstanceOf(ValidationError);
      expect((e as ValidationError).status).toBe(400);
    }
  });
});

describe("skipDependents", () => {
  const diamond: WorkflowStep[] = [
    { id: "A", command: "x" },
    { id: "B", command: "x", dependsOn: ["A"] },
    { id: "C", command: "x", dependsOn: ["A"] },
    { id: "D", command: "x", dependsOn: ["B", "C"] },
    { id: "E", command: "x" },
  ];

  test("skips the whole downstream cone", () => {
    const s = state(diamond, { A: "FAILED" });
    const skipped = skipDependents(s, "A");
    expect(skipped.sort()).toEqual(["B", "C", "D"]);
    expect(s.stepState["B"]?.status).toBe("SKIPPED");
    expect(s.stepState["C"]?.status).toBe("SKIPPED");
    expect(s.stepState["D"]?.status).toBe("SKIPPED");
  });

  test("does not touch unrelated branches", () => {
    const s = state(diamond, { A: "FAILED" });
    skipDependents(s, "A");
    expect(s.stepState["E"]?.status).toBe("PENDING");
  });

  test("propagates transitively through several layers", () => {
    const chain: WorkflowStep[] = [
      { id: "A", command: "x" },
      { id: "B", command: "x", dependsOn: ["A"] },
      { id: "C", command: "x", dependsOn: ["B"] },
      { id: "D", command: "x", dependsOn: ["C"] },
    ];
    const s = state(chain, { A: "FAILED" });
    skipDependents(s, "A");
    expect(["B", "C", "D"].every((id) => s.stepState[id]?.status === "SKIPPED")).toBe(true);
  });

  // A step that already completed keeps its real result. Overwriting a
  // COMPLETED step with SKIPPED would erase work that genuinely succeeded.
  test("never overwrites an already-terminal step", () => {
    const s = state(diamond, { A: "FAILED", B: "COMPLETED" });
    skipDependents(s, "A");
    expect(s.stepState["B"]?.status).toBe("COMPLETED");
    // but D is still skipped, because C (its other dependency) failed
    expect(s.stepState["D"]?.status).toBe("SKIPPED");
  });

  test("records why the step was skipped", () => {
    const s = state(diamond, { A: "FAILED" });
    skipDependents(s, "A");
    expect(s.stepState["B"]?.error).toContain('"A"');
  });

  test("terminates on a graph with a cycle in state (no infinite loop)", () => {
    const cyclic: WorkflowStep[] = [
      { id: "A", command: "x", dependsOn: ["B"] },
      { id: "B", command: "x", dependsOn: ["A"] },
    ];
    const s = state(cyclic, {});
    s.stepState["A"]!.status = "FAILED";
    // Should return rather than loop forever. The `visited` guard in the real
    // implementation is what makes this safe.
    const result = skipDependents(s, "A");
    expect(Array.isArray(result)).toBe(true);
  });
});

describe("topoLayers", () => {
  test("groups a diamond into three layers", () => {
    const layers = topoLayers([
      { id: "A", command: "x" },
      { id: "B", command: "x", dependsOn: ["A"] },
      { id: "C", command: "x", dependsOn: ["A"] },
      { id: "D", command: "x", dependsOn: ["B", "C"] },
    ]);
    expect(layers).toEqual([["A"], ["B", "C"], ["D"]]);
  });

  test("independent steps share one layer", () => {
    const layers = topoLayers([
      { id: "A", command: "x" },
      { id: "B", command: "x" },
      { id: "C", command: "x" },
    ]);
    expect(layers).toHaveLength(1);
    expect(layers[0]).toHaveLength(3);
  });

  test("layer count equals critical path length", () => {
    const chain = Array.from({ length: 5 }, (_, i) => ({
      id: `s${i}`,
      command: "x",
      dependsOn: i === 0 ? [] : [`s${i - 1}`],
    }));
    expect(topoLayers(chain)).toHaveLength(5);
  });
});