package claude

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

const postResultTimeout = 5 * time.Second

// Driver implements agent.AgentRunner using the Claude Code CLI.
type Driver struct {
	pm     ProcessManager
	logger *slog.Logger
}

func NewDriver(pm ProcessManager, logger *slog.Logger) *Driver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Driver{pm: pm, logger: logger}
}

func (d *Driver) Run(ctx context.Context, opts types.RunOpts) (*types.RunResult, error) {
	args := []string{
		"-p", opts.Prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if len(opts.AllowedTools) > 0 {
		for _, tool := range opts.AllowedTools {
			args = append(args, "--allowedTools", tool)
		}
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	return d.execute(ctx, opts.WorkspacePath, args, opts)
}

func (d *Driver) Continue(ctx context.Context, sessionID string, prompt string) (*types.RunResult, error) {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--resume", sessionID,
	}
	return d.execute(ctx, "", args, types.RunOpts{})
}

func (d *Driver) Kill(pid int) error {
	// Construct a minimal cmd to pass to ProcessManager.Kill
	// In practice, the orchestrator holds the cmd reference from Run
	d.logger.Warn("Kill called directly with pid", "pid", pid)
	return fmt.Errorf("direct pid kill not supported, use context cancellation")
}

func (d *Driver) execute(ctx context.Context, workDir string, args []string, opts types.RunOpts) (*types.RunResult, error) {
	cmd := exec.CommandContext(ctx, "claude", args...)
	if workDir != "" {
		cmd.Dir = workDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}

	if err := d.pm.Start(cmd); err != nil {
		return nil, fmt.Errorf("starting claude process: %w", err)
	}

	start := time.Now()
	var result *types.RunResult
	var parseErr error
	var lastActivity time.Time

	stallTimeout := time.Duration(opts.StallTimeoutMS) * time.Millisecond
	if stallTimeout == 0 {
		stallTimeout = 5 * time.Minute
	}

	// Monitor for stall in a separate goroutine
	var mu sync.Mutex
	lastActivity = time.Now()
	done := make(chan struct{})
	defer close(done)

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				mu.Lock()
				idle := time.Since(lastActivity)
				mu.Unlock()
				if idle > stallTimeout {
					d.logger.Warn("claude process stalled, killing",
						"idle", idle,
						"timeout", stallTimeout,
					)
					_ = d.pm.Kill(cmd)
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		mu.Lock()
		lastActivity = time.Now()
		mu.Unlock()

		line := scanner.Bytes()
		event, err := ParseStreamEvent(line)
		if err != nil {
			d.logger.Debug("failed to parse stream event", "error", err, "line", string(line))
			continue
		}
		if event == nil {
			continue
		}

		if event.Type == "result" {
			exitCode := 0
			if event.IsError {
				exitCode = 1
			}
			tokensIn := 0
			tokensOut := 0
			if event.Usage != nil {
				tokensIn = event.Usage.InputTokens + event.Usage.CacheCreationInputTokens + event.Usage.CacheReadInputTokens
				tokensOut = event.Usage.OutputTokens
			}
			result = &types.RunResult{
				SessionID: event.SessionID,
				ExitCode:  exitCode,
				TokensIn:  tokensIn,
				TokensOut: tokensOut,
				CostUSD:   event.TotalCost,
				TurnsUsed: event.NumTurns,
				Duration:  time.Since(start),
			}
			d.logger.Debug("parsed result event",
				"session_id", event.SessionID,
				"cost_usd", event.TotalCost,
				"turns", event.NumTurns,
				"tokens_in", tokensIn,
				"tokens_out", tokensOut,
			)
			// Known bug: Claude Code may hang after final result event.
			go func() {
				time.Sleep(postResultTimeout)
				_ = d.pm.Terminate(cmd)
			}()
		}
	}

	// Wait for process to exit
	waitErr := cmd.Wait()

	if result != nil {
		result.Duration = time.Since(start)
		return result, nil
	}

	if parseErr != nil {
		return nil, fmt.Errorf("parsing claude output: %w", parseErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("claude process exited: %w", waitErr)
	}

	return &types.RunResult{
		ExitCode: -1,
		Duration: time.Since(start),
	}, nil
}
