package orchestrator

import "time"

// RetryInfo tracks per-task retry state for exponential backoff.
type RetryInfo struct {
	TaskID      string
	Attempts    int
	NextRetryAt time.Time
	LastError   string
}

const baseBackoff = 10 * time.Second

// computeBackoff returns min(10s * 2^(attempts-1), maxBackoffMS).
func computeBackoff(attempts int, maxBackoffMS int) time.Duration {
	if attempts <= 0 {
		return baseBackoff
	}
	backoff := baseBackoff
	for i := 1; i < attempts; i++ {
		backoff *= 2
	}
	maxBackoff := time.Duration(maxBackoffMS) * time.Millisecond
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}

// recordFailure increments retry attempts and sets the next retry time.
// Must be called without o.mu held (it acquires the lock).
func (o *Orchestrator) recordFailure(taskID string, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	info, ok := o.retryState[taskID]
	if !ok {
		info = &RetryInfo{TaskID: taskID}
		o.retryState[taskID] = info
	}
	info.Attempts++
	info.LastError = err.Error()
	info.NextRetryAt = time.Now().Add(computeBackoff(info.Attempts, o.cfg.Agent.MaxRetryBackoffMS))
}

// isRetryEligible returns true if the task can be dispatched (no backoff pending).
// Must be called without o.mu held.
func (o *Orchestrator) isRetryEligible(taskID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()

	info, ok := o.retryState[taskID]
	if !ok {
		return true
	}
	return time.Now().After(info.NextRetryAt)
}

// retryInfo returns the current retry info for a task, or nil. Caller must not hold o.mu.
func (o *Orchestrator) retryInfo(taskID string) *RetryInfo {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.retryState[taskID]
}

// clearRetry removes retry state on success. Must be called without o.mu held.
func (o *Orchestrator) clearRetry(taskID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.retryState, taskID)
}
