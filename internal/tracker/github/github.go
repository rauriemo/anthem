package github

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	gh "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"

	"github.com/rauriemo/anthem/internal/types"
)

// GitHubTracker implements tracker.IssueTracker using the GitHub API.
type GitHubTracker struct {
	client       *gh.Client
	owner        string
	repo         string
	activeLabels []string
	logger       *slog.Logger
	etag         string
}

type Options struct {
	Owner        string
	Repo         string
	ActiveLabels []string
	Logger       *slog.Logger
}

// New creates a GitHubTracker. Resolves auth via GITHUB_TOKEN or gh CLI.
func New(ctx context.Context, opts Options) (*GitHubTracker, error) {
	token, err := ResolveToken()
	if err != nil {
		return nil, fmt.Errorf("resolving github auth: %w", err)
	}
	return NewWithToken(ctx, token, opts), nil
}

// NewWithToken creates a GitHubTracker with an explicit token.
func NewWithToken(ctx context.Context, token string, opts Options) *GitHubTracker {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(ctx, ts)
	client := gh.NewClient(httpClient)

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &GitHubTracker{
		client:       client,
		owner:        opts.Owner,
		repo:         opts.Repo,
		activeLabels: opts.ActiveLabels,
		logger:       logger,
	}
}

// ParseRepo splits "owner/repo" into owner and repo.
func ParseRepo(fullRepo string) (owner, repo string, err error) {
	parts := strings.SplitN(fullRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format %q, expected owner/repo", fullRepo)
	}
	return parts[0], parts[1], nil
}

func (g *GitHubTracker) ListActive(ctx context.Context) ([]types.Task, error) {
	var allIssues []*gh.Issue

	for _, label := range g.activeLabels {
		opts := &gh.IssueListByRepoOptions{
			Labels: []string{label},
			State:  "open",
			ListOptions: gh.ListOptions{PerPage: 100},
		}

		for {
			issues, resp, err := g.client.Issues.ListByRepo(ctx, g.owner, g.repo, opts)
			if err != nil {
				return nil, fmt.Errorf("listing issues with label %q: %w", label, err)
			}
			g.checkRateLimit(resp)

			for _, issue := range issues {
				if issue.IsPullRequest() {
					continue
				}
				allIssues = append(allIssues, issue)
			}

			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}

	seen := make(map[int64]bool)
	var tasks []types.Task
	for _, issue := range allIssues {
		if seen[issue.GetID()] {
			continue
		}
		seen[issue.GetID()] = true
		tasks = append(tasks, issueToTask(issue, g.owner, g.repo))
	}
	return tasks, nil
}

func (g *GitHubTracker) GetTask(ctx context.Context, id string) (*types.Task, error) {
	num, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("invalid issue number %q: %w", id, err)
	}

	issue, resp, err := g.client.Issues.Get(ctx, g.owner, g.repo, num)
	if err != nil {
		return nil, fmt.Errorf("getting issue #%d: %w", num, err)
	}
	g.checkRateLimit(resp)

	task := issueToTask(issue, g.owner, g.repo)
	return &task, nil
}

func (g *GitHubTracker) UpdateStatus(ctx context.Context, id string, status string) error {
	num, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid issue number %q: %w", id, err)
	}

	state := "open"
	if types.Status(status).IsTerminal() {
		state = "closed"
	}

	_, resp, err := g.client.Issues.Edit(ctx, g.owner, g.repo, num, &gh.IssueRequest{
		State: gh.Ptr(state),
	})
	if err != nil {
		return fmt.Errorf("updating issue #%d status: %w", num, err)
	}
	g.checkRateLimit(resp)
	return nil
}

func (g *GitHubTracker) AddComment(ctx context.Context, id string, body string) error {
	num, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid issue number %q: %w", id, err)
	}

	_, resp, err := g.client.Issues.CreateComment(ctx, g.owner, g.repo, num, &gh.IssueComment{
		Body: gh.Ptr(body),
	})
	if err != nil {
		return fmt.Errorf("adding comment to issue #%d: %w", num, err)
	}
	g.checkRateLimit(resp)
	return nil
}

func (g *GitHubTracker) AddLabel(ctx context.Context, id string, label string) error {
	num, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid issue number %q: %w", id, err)
	}

	_, resp, err := g.client.Issues.AddLabelsToIssue(ctx, g.owner, g.repo, num, []string{label})
	if err != nil {
		return fmt.Errorf("adding label %q to issue #%d: %w", label, num, err)
	}
	g.checkRateLimit(resp)
	return nil
}

func (g *GitHubTracker) RemoveLabel(ctx context.Context, id string, label string) error {
	num, err := strconv.Atoi(id)
	if err != nil {
		return fmt.Errorf("invalid issue number %q: %w", id, err)
	}

	resp, err := g.client.Issues.RemoveLabelForIssue(ctx, g.owner, g.repo, num, label)
	if err != nil {
		return fmt.Errorf("removing label %q from issue #%d: %w", label, num, err)
	}
	g.checkRateLimit(resp)
	return nil
}

func (g *GitHubTracker) checkRateLimit(resp *gh.Response) {
	if resp == nil {
		return
	}
	remaining := resp.Rate.Remaining
	limit := resp.Rate.Limit
	if limit > 0 && remaining < limit/10 {
		reset := resp.Rate.Reset.Time
		g.logger.Warn("github rate limit low",
			"remaining", remaining,
			"limit", limit,
			"reset", reset.Format(time.RFC3339),
		)
	}
}

func issueToTask(issue *gh.Issue, owner, repo string) types.Task {
	var labels []string
	for _, l := range issue.Labels {
		labels = append(labels, l.GetName())
	}

	identifier := fmt.Sprintf("GH-%d", issue.GetNumber())

	body := issue.GetBody()
	title := issue.GetTitle()
	createdAt := issue.GetCreatedAt().Time

	return types.Task{
		ID:         strconv.Itoa(issue.GetNumber()),
		Identifier: identifier,
		Title:      title,
		Body:       body,
		Labels:     labels,
		Status:     types.StatusActive,
		CreatedAt:  createdAt,
		RepoURL:    fmt.Sprintf("https://github.com/%s/%s", owner, repo),
		Metadata:   make(map[string]string),
	}
}
