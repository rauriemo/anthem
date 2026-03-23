package types

import (
	"strings"
	"testing"
)

func TestTransition_Legal(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
	}{
		{StatusQueued, StatusPlanned},
		{StatusQueued, StatusRunning},
		{StatusQueued, StatusCanceled},
		{StatusQueued, StatusSkipped},
		{StatusPlanned, StatusRunning},
		{StatusPlanned, StatusSkipped},
		{StatusPlanned, StatusCanceled},
		{StatusRunning, StatusCompleted},
		{StatusRunning, StatusFailed},
		{StatusRunning, StatusRetryQueued},
		{StatusRunning, StatusBlocked},
		{StatusRunning, StatusCanceled},
		{StatusRetryQueued, StatusRunning},
		{StatusBlocked, StatusQueued},
		{StatusBlocked, StatusNeedsApproval},
		{StatusNeedsApproval, StatusQueued},
		{StatusNeedsApproval, StatusCanceled},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			if err := Transition(tt.from, tt.to); err != nil {
				t.Fatalf("expected legal transition %s -> %s, got error: %v", tt.from, tt.to, err)
			}
		})
	}
}

func TestTransition_Illegal(t *testing.T) {
	tests := []struct {
		from Status
		to   Status
	}{
		{StatusQueued, StatusCompleted},
		{StatusQueued, StatusFailed},
		{StatusQueued, StatusBlocked},
		{StatusPlanned, StatusCompleted},
		{StatusPlanned, StatusQueued},
		{StatusRunning, StatusQueued},
		{StatusRunning, StatusPlanned},
		{StatusRetryQueued, StatusCompleted},
		{StatusRetryQueued, StatusFailed},
		{StatusBlocked, StatusRunning},
		{StatusBlocked, StatusCompleted},
		{StatusNeedsApproval, StatusRunning},
		{StatusCompleted, StatusRunning},
		{StatusFailed, StatusRunning},
		{StatusCanceled, StatusQueued},
		{StatusSkipped, StatusQueued},
	}

	for _, tt := range tests {
		t.Run(string(tt.from)+"->"+string(tt.to), func(t *testing.T) {
			err := Transition(tt.from, tt.to)
			if err == nil {
				t.Fatalf("expected error for illegal transition %s -> %s", tt.from, tt.to)
			}
			if !strings.Contains(err.Error(), "invalid transition") {
				t.Fatalf("error %q should contain 'invalid transition'", err.Error())
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		status Status
		want   bool
	}{
		{StatusQueued, false},
		{StatusPlanned, false},
		{StatusRunning, false},
		{StatusBlocked, false},
		{StatusRetryQueued, false},
		{StatusNeedsApproval, false},
		{StatusCompleted, true},
		{StatusFailed, true},
		{StatusCanceled, true},
		{StatusSkipped, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := tt.status.IsTerminal(); got != tt.want {
				t.Fatalf("IsTerminal(%s) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestStatusToLabel(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusQueued, "todo"},
		{StatusPlanned, "todo"},
		{StatusRetryQueued, "todo"},
		{StatusRunning, "in-progress"},
		{StatusBlocked, "blocked"},
		{StatusNeedsApproval, "needs-approval"},
		{StatusSkipped, "skipped"},
		{StatusCompleted, "done"},
		{StatusFailed, "failed"},
		{StatusCanceled, "canceled"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if got := StatusToLabel(tt.status); got != tt.want {
				t.Fatalf("StatusToLabel(%s) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestLabelToStatus(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   Status
	}{
		{"in-progress", []string{"in-progress"}, StatusRunning},
		{"blocked", []string{"blocked"}, StatusBlocked},
		{"needs-approval", []string{"needs-approval"}, StatusNeedsApproval},
		{"done", []string{"done"}, StatusCompleted},
		{"failed", []string{"failed"}, StatusFailed},
		{"canceled", []string{"canceled"}, StatusCanceled},
		{"skipped", []string{"skipped"}, StatusSkipped},
		{"todo", []string{"todo"}, StatusQueued},
		{"empty labels default", []string{}, StatusQueued},
		{"nil labels default", nil, StatusQueued},
		{"in-progress beats todo", []string{"todo", "in-progress"}, StatusRunning},
		{"blocked beats done", []string{"done", "blocked"}, StatusBlocked},
		{"in-progress beats blocked", []string{"blocked", "in-progress"}, StatusRunning},
		{"unknown labels default", []string{"feature", "urgent"}, StatusQueued},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LabelToStatus(tt.labels); got != tt.want {
				t.Fatalf("LabelToStatus(%v) = %s, want %s", tt.labels, got, tt.want)
			}
		})
	}
}
