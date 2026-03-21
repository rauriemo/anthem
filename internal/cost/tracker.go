package cost

import "sync"

// Tracker aggregates token usage and cost across agent sessions.
type Tracker struct {
	mu       sync.Mutex
	sessions map[string]*SessionCost
}

type SessionCost struct {
	TaskID    string
	SessionID string
	TokensIn  int
	TokensOut int
	CostUSD   float64
	TurnsUsed int
}

func NewTracker() *Tracker {
	return &Tracker{sessions: make(map[string]*SessionCost)}
}

func (t *Tracker) Record(sc SessionCost) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sessions[sc.SessionID] = &sc
}

func (t *Tracker) TotalCost() float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total float64
	for _, sc := range t.sessions {
		total += sc.CostUSD
	}
	return total
}

func (t *Tracker) TaskCost(taskID string) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	var total float64
	for _, sc := range t.sessions {
		if sc.TaskID == taskID {
			total += sc.CostUSD
		}
	}
	return total
}
