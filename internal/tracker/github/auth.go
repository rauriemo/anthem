package github

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ResolveToken returns a GitHub token using GITHUB_TOKEN env var,
// falling back to the gh CLI's stored token.
func ResolveToken() (string, error) {
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}

	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("GITHUB_TOKEN not set and gh auth token failed: %w", err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("GITHUB_TOKEN not set and gh auth token returned empty")
	}
	return token, nil
}
