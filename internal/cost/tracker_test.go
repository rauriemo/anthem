package cost

import (
	"math"
	"testing"
)

const epsilon = 1e-9

func approxEqual(a, b float64) bool {
	return math.Abs(a-b) < epsilon
}

func TestRecord(t *testing.T) {
	tr := NewTracker()
	tr.Record(SessionCost{
		TaskID:    "1",
		SessionID: "s1",
		TokensIn:  500,
		TokensOut: 200,
		CostUSD:   0.05,
		TurnsUsed: 3,
	})

	if got := tr.TotalCost(); got != 0.05 {
		t.Errorf("TotalCost() = %f, want 0.05", got)
	}
}

func TestTotalCost(t *testing.T) {
	tests := []struct {
		name     string
		sessions []SessionCost
		want     float64
	}{
		{
			name:     "empty tracker",
			sessions: nil,
			want:     0,
		},
		{
			name: "single session",
			sessions: []SessionCost{
				{SessionID: "s1", CostUSD: 0.10},
			},
			want: 0.10,
		},
		{
			name: "multiple sessions",
			sessions: []SessionCost{
				{SessionID: "s1", CostUSD: 0.10},
				{SessionID: "s2", CostUSD: 0.25},
				{SessionID: "s3", CostUSD: 0.05},
			},
			want: 0.40,
		},
		{
			name: "overwrite same session ID",
			sessions: []SessionCost{
				{SessionID: "s1", CostUSD: 0.10},
				{SessionID: "s1", CostUSD: 0.50},
			},
			want: 0.50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewTracker()
			for _, sc := range tt.sessions {
				tr.Record(sc)
			}
			if got := tr.TotalCost(); !approxEqual(got, tt.want) {
				t.Errorf("TotalCost() = %f, want %f", got, tt.want)
			}
		})
	}
}

func TestTaskCost(t *testing.T) {
	tests := []struct {
		name     string
		sessions []SessionCost
		taskID   string
		want     float64
	}{
		{
			name:   "no sessions for task",
			taskID: "1",
			want:   0,
		},
		{
			name: "single session for task",
			sessions: []SessionCost{
				{TaskID: "1", SessionID: "s1", CostUSD: 0.10},
				{TaskID: "2", SessionID: "s2", CostUSD: 0.20},
			},
			taskID: "1",
			want:   0.10,
		},
		{
			name: "multiple sessions for same task",
			sessions: []SessionCost{
				{TaskID: "1", SessionID: "s1", CostUSD: 0.10},
				{TaskID: "1", SessionID: "s2", CostUSD: 0.15},
				{TaskID: "2", SessionID: "s3", CostUSD: 0.50},
			},
			taskID: "1",
			want:   0.25,
		},
		{
			name: "task with no matching sessions",
			sessions: []SessionCost{
				{TaskID: "2", SessionID: "s1", CostUSD: 0.10},
			},
			taskID: "999",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := NewTracker()
			for _, sc := range tt.sessions {
				tr.Record(sc)
			}
			if got := tr.TaskCost(tt.taskID); !approxEqual(got, tt.want) {
				t.Errorf("TaskCost(%q) = %f, want %f", tt.taskID, got, tt.want)
			}
		})
	}
}
