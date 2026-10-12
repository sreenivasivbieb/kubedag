package runtime

import (
	"errors"
	"testing"
	"time"
)

func TestLeaseValid(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name  string
		lease Lease
		want  bool
	}{
		{
			name:  "fresh lease",
			lease: Lease{AcquiredAt: now, TTL: time.Minute},
			want:  true,
		},
		{
			name:  "lease just inside ttl",
			lease: Lease{AcquiredAt: now.Add(-59 * time.Second), TTL: time.Minute},
			want:  true,
		},
		{
			name:  "lease at exact ttl boundary is expired",
			lease: Lease{AcquiredAt: now.Add(-time.Minute), TTL: time.Minute},
			want:  false,
		},
		{
			name:  "lease well past ttl",
			lease: Lease{AcquiredAt: now.Add(-time.Hour), TTL: time.Minute},
			want:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.lease.Valid(now); got != tc.want {
				t.Errorf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExecResultSucceeded(t *testing.T) {
	tests := []struct {
		name string
		res  ExecResult
		want bool
	}{
		{"exit zero", ExecResult{ExitCode: 0}, true},
		{"nonzero exit", ExecResult{ExitCode: 1}, false},
		{"killed by signal 137", ExecResult{ExitCode: 137}, false},
		{"timed out", ExecResult{TimedOut: true}, false},
		{"infra error", ExecResult{Err: errors.New("pod gone")}, false},
		{"infra error despite exit zero", ExecResult{ExitCode: 0, Err: errors.New("stream broke")}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.res.Succeeded(); got != tc.want {
				t.Errorf("Succeeded() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A timeout and a non-zero exit are different verdicts. Conflating them would
// make Phase 6's retry classifier unable to distinguish "the user's program
// said no" from "we killed it because it ran too long", which have different
// retry policies.
func TestExecResultOutcome(t *testing.T) {
	tests := []struct {
		name string
		res  ExecResult
		want string
	}{
		{"clean success", ExecResult{ExitCode: 0}, "SUCCEEDED"},
		{"user program failed", ExecResult{ExitCode: 2}, "FAILED"},
		{"timeout", ExecResult{ExitCode: -1, TimedOut: true}, "TIMED_OUT"},
		{"infra failure", ExecResult{Err: errors.New("node not found")}, "FAILED"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(tc.res.Outcome()); got != tc.want {
				t.Errorf("Outcome() = %s, want %s", got, tc.want)
			}
		})
	}
}
