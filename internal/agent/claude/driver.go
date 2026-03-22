package claude

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

	stallTimeout := time.Duration(opts.StallTimeoutMS) * time.Millisecond
	if stallTimeout == 0 {
		stallTimeout = 5 * time.Minute
	}

	start := time.Now()

	// Monitor for stall in a separate goroutine
	var mu sync.Mutex
	lastActivity := time.Now()
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

	onActivity := func() {
		mu.Lock()
		lastActivity = time.Now()
		mu.Unlock()
	}

	result, scanErr := d.parseStdout(ctx, stdout, start, stallTimeout, onActivity, func() {
		go func() {
			time.Sleep(postResultTimeout)
			_ = d.pm.Terminate(cmd)
		}()
	})

	waitErr := cmd.Wait()

	if result != nil {
		result.Duration = time.Since(start)
		return result, nil
	}
	if scanErr != nil {
		return nil, fmt.Errorf("reading claude output: %w", scanErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("claude process exited: %w", waitErr)
	}

	return &types.RunResult{
		ExitCode: -1,
		Duration: time.Since(start),
	}, nil
}

// parseStdout reads stream-json lines from r and extracts the result event.
// onActivity is called on each line read. onResult is called when a result event is found.
// Returns the parsed result (or nil) and any scanner error.
func (d *Driver) parseStdout(ctx context.Context, r io.Reader, start time.Time, stallTimeout time.Duration, onActivity func(), onResult func()) (*types.RunResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	var result *types.RunResult

	for scanner.Scan() {
		if onActivity != nil {
			onActivity()
		}

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
			if onResult != nil {
				onResult()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		d.logger.Warn("error reading claude stdout", "error", err)
		return result, err
	}

	return result, nil
}
