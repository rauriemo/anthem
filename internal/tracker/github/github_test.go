package github

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v68/github"
)

func TestParseRepo(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{name: "valid", input: "user/repo", wantOwner: "user", wantRepo: "repo"},
		{name: "org repo", input: "my-org/my-repo", wantOwner: "my-org", wantRepo: "my-repo"},
		{name: "missing slash", input: "noslash", wantErr: true},
		{name: "empty owner", input: "/repo", wantErr: true},
		{name: "empty repo", input: "owner/", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseRepo(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", owner, tt.wantOwner)
			}
			if repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", repo, tt.wantRepo)
			}
		})
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func issueJSON(number int, title string, labels []string) map[string]any {
	var labelObjs []map[string]any
	for _, l := range labels {
		labelObjs = append(labelObjs, map[string]any{"id": 1, "name": l})
	}
	return map[string]any{
		"id":         number * 100,
		"number":     number,
		"title":      title,
		"body":       "body",
		"state":      "open",
		"labels":     labelObjs,
		"created_at": "2024-01-01T00:00:00Z",
	}
}

func newTestTracker(t *testing.T, serverURL string) *GitHubTracker {
	t.Helper()
	u, _ := url.Parse(serverURL + "/")
	tracker := newWithHTTPClient(&http.Client{}, Options{
		Owner:        "test",
		Repo:         "repo",
		ActiveLabels: []string{"todo"},
		Logger:       testLogger(),
	})
	tracker.client.BaseURL = u
	return tracker
}

func TestListActiveETag(t *testing.T) {
	tests := []struct {
		name          string
		wantCached    bool
		wantTaskCount int
		wantCallCount int32
	}{
		{
			name:          "first call returns fresh data, second returns cached on 304",
			wantCached:    true,
			wantTaskCount: 1,
			wantCallCount: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var callCount atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount.Add(1)

				if r.Header.Get("If-None-Match") == `"test-etag"` {
					w.WriteHeader(http.StatusNotModified)
					return
				}

				w.Header().Set("ETag", `"test-etag"`)
				issues := []map[string]any{issueJSON(1, "Test Issue", []string{"todo"})}
				json.NewEncoder(w).Encode(issues)
			}))
			defer srv.Close()

			tracker := newTestTracker(t, srv.URL)
			ctx := context.Background()

			tasks, err := tracker.ListActive(ctx)
			if err != nil {
				t.Fatalf("first ListActive failed: %v", err)
			}
			if len(tasks) != tt.wantTaskCount {
				t.Errorf("first call: got %d tasks, want %d", len(tasks), tt.wantTaskCount)
			}

			tasks2, err := tracker.ListActive(ctx)
			if err != nil {
				t.Fatalf("second ListActive failed: %v", err)
			}
			if len(tasks2) != tt.wantTaskCount {
				t.Errorf("second call: got %d tasks, want %d", len(tasks2), tt.wantTaskCount)
			}

			if callCount.Load() != tt.wantCallCount {
				t.Errorf("server called %d times, want %d", callCount.Load(), tt.wantCallCount)
			}

			if tasks[0].Title != tasks2[0].Title {
				t.Errorf("cached task title = %q, want %q", tasks2[0].Title, tasks[0].Title)
			}
		})
	}
}

func TestListActiveETagSentOnSecondCall(t *testing.T) {
	var receivedEtag string
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n == 2 {
			receivedEtag = r.Header.Get("If-None-Match")
		}

		w.Header().Set("ETag", `"abc123"`)
		issues := []map[string]any{issueJSON(1, "Issue", []string{"todo"})}
		json.NewEncoder(w).Encode(issues)
	}))
	defer srv.Close()

	tracker := newTestTracker(t, srv.URL)
	ctx := context.Background()
	tracker.ListActive(ctx)
	tracker.ListActive(ctx)

	if receivedEtag != `"abc123"` {
		t.Errorf("second call ETag = %q, want %q", receivedEtag, `"abc123"`)
	}
}

func TestShouldThrottle(t *testing.T) {
	tests := []struct {
		name          string
		throttleUntil time.Time
		wantThrottled bool
	}{
		{
			name:          "not throttled when zero",
			throttleUntil: time.Time{},
			wantThrottled: false,
		},
		{
			name:          "throttled when reset is in future",
			throttleUntil: time.Now().Add(10 * time.Minute),
			wantThrottled: true,
		},
		{
			name:          "not throttled after reset time passed",
			throttleUntil: time.Now().Add(-1 * time.Second),
			wantThrottled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := &GitHubTracker{
				throttleUntil: tt.throttleUntil,
				logger:        testLogger(),
			}

			throttled, dur := tracker.ShouldThrottle()
			if throttled != tt.wantThrottled {
				t.Errorf("ShouldThrottle() = %v, want %v", throttled, tt.wantThrottled)
			}
			if tt.wantThrottled && dur <= 0 {
				t.Errorf("expected positive duration when throttled, got %v", dur)
			}
			if !tt.wantThrottled && dur != 0 {
				t.Errorf("expected zero duration when not throttled, got %v", dur)
			}
		})
	}
}

func TestCheckRateLimitSetsThrottle(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		remaining     int
		wantThrottled bool
	}{
		{
			name:          "throttles when remaining below threshold",
			limit:         100,
			remaining:     5,
			wantThrottled: true,
		},
		{
			name:          "does not throttle when remaining above threshold",
			limit:         100,
			remaining:     50,
			wantThrottled: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tracker := &GitHubTracker{
				logger: testLogger(),
			}

			resetTime := time.Now().Add(5 * time.Minute)
			resp := &gh.Response{
				Rate: gh.Rate{
					Limit:     tt.limit,
					Remaining: tt.remaining,
					Reset:     gh.Timestamp{Time: resetTime},
				},
			}

			tracker.checkRateLimit(resp)

			throttled, _ := tracker.ShouldThrottle()
			if throttled != tt.wantThrottled {
				t.Errorf("ShouldThrottle() = %v, want %v", throttled, tt.wantThrottled)
			}
		})
	}
}
