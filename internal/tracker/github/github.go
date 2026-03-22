package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"

	"github.com/rauriemo/anthem/internal/types"
)

// etagTransport injects If-None-Match headers for conditional requests.
type etagTransport struct {
	base  http.RoundTripper
	mu    sync.Mutex
	etags map[string]string // URL -> ETag
}

func (t *etagTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.String()
	t.mu.Lock()
	etag := t.etags[key]
	t.mu.Unlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if newEtag := resp.Header.Get("ETag"); newEtag != "" {
		t.mu.Lock()
		t.etags[key] = newEtag
		t.mu.Unlock()
	}
	return resp, nil
}

// GitHubTracker implements tracker.IssueTracker using the GitHub API.
type GitHubTracker struct {
	client        *gh.Client
	owner         string
	repo          string
	activeLabels  []string
	logger        *slog.Logger
	lastTasks     []types.Task // cached result from last successful ListActive
	throttleUntil time.Time
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
	oauthClient := oauth2.NewClient(ctx, ts)

	et := &etagTransport{
		base:  oauthClient.Transport,
		etags: make(map[string]string),
	}
	oauthClient.Transport = et
	client := gh.NewClient(oauthClient)

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

// newWithHTTPClient creates a GitHubTracker with a custom HTTP client (for testing).
// It wraps the client's transport with etagTransport for conditional requests.
func newWithHTTPClient(httpClient *http.Client, opts Options) *GitHubTracker {
	et := &etagTransport{
		base:  httpClient.Transport,
		etags: make(map[string]string),
	}
	if et.base == nil {
		et.base = http.DefaultTransport
	}
	httpClient.Transport = et
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
	allCached := true

	for _, label := range g.activeLabels {
		opts := &gh.IssueListByRepoOptions{
			Labels:      []string{label},
			State:       "open",
			ListOptions: gh.ListOptions{PerPage: 100},
		}

		cachedLabel := false
		for {
			issues, resp, err := g.client.Issues.ListByRepo(ctx, g.owner, g.repo, opts)
			if err != nil {
				if isNotModified(err) {
					g.logger.Debug("github returned 304, using cached tasks", "label", label)
					cachedLabel = true
					break
				}
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
		if !cachedLabel {
			allCached = false
		}
	}

	if allCached && g.lastTasks != nil {
		return g.lastTasks, nil
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
	g.lastTasks = tasks
	return tasks, nil
}

func isNotModified(err error) bool {
	var errResp *gh.ErrorResponse
	if errors.As(err, &errResp) {
		return errResp.Response != nil && errResp.Response.StatusCode == http.StatusNotModified
	}
	return false
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
		g.throttleUntil = reset
		g.logger.Warn("github rate limit low",
			"remaining", remaining,
			"limit", limit,
			"reset", reset.Format(time.RFC3339),
		)
	}
}

// ShouldThrottle returns true and the wait duration if the tracker is
// currently throttled due to low API rate limit.
func (g *GitHubTracker) ShouldThrottle() (bool, time.Duration) {
	if g.throttleUntil.IsZero() {
		return false, 0
	}
	remaining := time.Until(g.throttleUntil)
	if remaining <= 0 {
		g.throttleUntil = time.Time{}
		return false, 0
	}
	return true, remaining
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
