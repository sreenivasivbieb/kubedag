// Package domain holds the pure, I/O-free core of kubedag: the workflow
// definition language, the run/step state machines, and the runtime contract.
//
// Nothing in this package may import a database driver, an HTTP server, or a
// Kubernetes client. That constraint is what makes the interesting logic
// (validation, cycle detection, skip propagation, legal transitions) testable
// without fixtures and without a cluster.
package domain

import (
	"encoding/json"
	"time"
)

// ResourceSpec mirrors the subset of Kubernetes resource requirements that a
// workflow author may set. We deliberately do not accept arbitrary PodSpec
// fragments: the step spec is a constrained vocabulary, not an escape hatch.
type ResourceSpec struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}

// IsZero reports whether no resource requests or limits were specified.
func (r ResourceSpec) IsZero() bool { return r.CPU == "" && r.Memory == "" }

// StepSpec is the author's declaration of a single node in the DAG. It is
// immutable once a run is created: retries re-execute a StepSpec, they never
// rewrite it.
type StepSpec struct {
	ID string `json:"id" yaml:"id"`
	// Image is optional. When empty, the command runs in the shared runner
	// image. Exactly one of Image or an inline Command is enough; when both
	// are set, Image wins and Command overrides the image entrypoint.
	Image   string            `json:"image,omitempty" yaml:"image,omitempty"`
	Command []string          `json:"command" yaml:"command"`
	Env     map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// DependsOn lists step IDs that must SUCCEED first. Empty means "ready
	// as soon as the run starts".
	DependsOn []string     `json:"dependsOn,omitempty" yaml:"dependsOn,omitempty"`
	Resources ResourceSpec `json:"resources,omitempty" yaml:"resources,omitempty"`
	Retries   int          `json:"retries,omitempty" yaml:"retries,omitempty"`
	// TimeoutSeconds bounds a single attempt. Zero means "use the run default".
	TimeoutSeconds int `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
	// ContinueOnFailure lets the run proceed past this step's failure instead
	// of skipping all downstream steps.
	ContinueOnFailure bool `json:"continueOnFailure,omitempty" yaml:"continueOnFailure,omitempty"`
}

// WorkflowSpec is the author-supplied, unvalidated definition. Validate()
// turns this into a Workflow, which is the only form the engine will run.
type WorkflowSpec struct {
	ID    string            `json:"id" yaml:"id"`
	Env   map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Steps []StepSpec        `json:"steps" yaml:"steps"`
}

// Workflow is a validated DAG. Invariant: every ID in DependsOn refers to a
// step in Steps, and the graph is acyclic. Construct one only via Validate().
type Workflow struct {
	ID    string            `json:"id"`
	Env   map[string]string `json:"env,omitempty"`
	Steps []StepSpec        `json:"steps"`
	// byID and order are derived indices, built once by Validate.
	byID  map[string]int
	order []int
}

// StepByID returns the spec for the given step ID.
func (w *Workflow) StepByID(id string) (StepSpec, bool) {
	i, ok := w.byID[id]
	if !ok {
		return StepSpec{}, false
	}
	return w.Steps[i], true
}

// StepIDs returns step IDs in declaration order.
func (w *Workflow) StepIDs() []string {
	out := make([]string, 0, len(w.order))
	for _, i := range w.order {
		out = append(out, w.Steps[i].ID)
	}
	return out
}

// Dependents returns the IDs of steps that directly depend on id.
// Derived from Steps, so it is O(n) per call; callers that need this in a hot
// loop should build the reverse index once via DependentsIndex.
func (w *Workflow) Dependents(id string) []string {
	var out []string
	for _, s := range w.Steps {
		for _, d := range s.DependsOn {
			if d == id {
				out = append(out, s.ID)
				break
			}
		}
	}
	return out
}

// RunStatus is the persisted lifecycle of a whole run.
type RunStatus string

const (
	RunPending   RunStatus = "PENDING"
	RunRunning   RunStatus = "RUNNING"
	RunSucceeded RunStatus = "SUCCEEDED"
	RunFailed    RunStatus = "FAILED"
	RunCancelled RunStatus = "CANCELLED"
)

// IsTerminal reports whether the run can never change state again.
// Terminal is final: no transition out of a terminal run status is legal,
// in the state machine or in reconciliation.
func (r RunStatus) IsTerminal() bool {
	switch r {
	case RunSucceeded, RunFailed, RunCancelled:
		return true
	default:
		return false
	}
}

// StepStatus is the persisted lifecycle of one step within a run.
type StepStatus string

const (
	StepPending   StepStatus = "PENDING"
	StepQueued    StepStatus = "QUEUED"
	StepRunning   StepStatus = "RUNNING"
	StepSucceeded StepStatus = "SUCCEEDED"
	StepFailed    StepStatus = "FAILED"
	StepTimedOut  StepStatus = "TIMED_OUT"
	StepSkipped   StepStatus = "SKIPPED"
	StepCancelled StepStatus = "CANCELLED"
)

// IsTerminal reports whether this step status is final. Terminal step states
// are never re-entered; a retry moves a step back to QUEUED only by way of an
// explicit user-initiated rerun, which bumps the step's generation.
func (s StepStatus) IsTerminal() bool {
	switch s {
	case StepSucceeded, StepFailed, StepTimedOut, StepSkipped, StepCancelled:
		return true
	default:
		return false
	}
}

// IsSuccess reports whether the step completed successfully. Only SUCCEEDED
// satisfies dependencies; TIMED_OUT is a failure, not a soft success.
func (s StepStatus) IsSuccess() bool { return s == StepSucceeded }

// Attempt records one physical execution of a step. A step with Retries=2 may
// have up to three attempts, each with a distinct AttemptID.
//
// Note there is no FencingToken counter. Freshness is determined by
// (Attempt, PodID) against the step row — see Fence and ADR 0003. A separate
// monotonic counter would need an allocator that stays consistent across
// crashes, for no additional guarantee.
type Attempt struct {
	AttemptID string     `json:"attemptId"`
	Number    int        `json:"number"`
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
	Pod       string     `json:"pod,omitempty"`
	Node      string     `json:"node,omitempty"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	Outcome   StepStatus `json:"outcome"`
	Error     string     `json:"error,omitempty"`
	// Stdout and Stderr are trimmed and bounded by MaxLogBytes at write time.
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
}

// MarshalLogLine renders an attempt's output for the logs endpoint.
func (a Attempt) MarshalLogLine() ([]byte, error) {
	return json.Marshal(a)
}
