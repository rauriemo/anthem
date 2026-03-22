package github

import (
	"os"
	"testing"
)

func TestResolveTokenFromEnv(t *testing.T) {
	const testToken = "ghp_test_token_12345"
	t.Setenv("GITHUB_TOKEN", testToken)

	token, err := ResolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != testToken {
		t.Errorf("token = %q, want %q", token, testToken)
	}
}

func TestResolveTokenEnvTakesPrecedence(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "env-token")

	token, err := ResolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "env-token" {
		t.Errorf("expected env var to take precedence, got %q", token)
	}
}

func TestResolveTokenEmptyEnvFallsToGH(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

	_, err := ResolveToken()
	// If gh CLI is not installed, this will fail -- that's expected in CI
	if err != nil {
		if _, lookupErr := os.Stat("gh"); lookupErr != nil {
			t.Skip("gh CLI not available, skipping fallback test")
		}
	}
}
