package rules

import "github.com/rauriemo/anthem/internal/types"

// NeedsApproval checks if a task has the required approval label.
func NeedsApproval(task types.Task, approvalLabel string) bool {
	for _, l := range task.Labels {
		if l == approvalLabel {
			return false
		}
	}
	return true
}
