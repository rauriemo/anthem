package tracker

import (
	"context"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

type IssueTracker interface {
	ListActive(ctx context.Context) ([]types.Task, error)
	GetTask(ctx context.Context, id string) (*types.Task, error)
	UpdateStatus(ctx context.Context, id string, status string) error
	AddComment(ctx context.Context, id string, body string) error
	AddLabel(ctx context.Context, id string, label string) error
	RemoveLabel(ctx context.Context, id string, label string) error
	ShouldThrottle() (bool, time.Duration)
}
