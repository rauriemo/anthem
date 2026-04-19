package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/rauriemo/anthem/internal/types"
)

const postResultTimeout = 5 * time.Second

// Driver implements agent.AgentRunner using the Claude Code CLI.
// The binary field controls which CLI executable is invoked (default: "claude").
type Driver struct {
	pm     ProcessManager
	logger *slog.Logger
	binary string
}

func NewDriver(pm ProcessManager, logger *slog.Logger, binary string) *Driver {
	if logger == nil {
		logger = slog.Default()
	}
	if binary == "" {
		binary = "claude"
	}
	return &Driver{pm: pm, logger: logger, binary: binary}
}

func (d *Driver) Run(ctx context.Context, opts types.RunOpts) (*types.RunResult, error) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	switch opts.PermissionMode {
	case "bypassPermissions":
		args = append(args, "--dangerously-skip-permissions")
	default:
		args = append(args, "--permission-mode", "dontAsk")
	}
	// ToolsDisabled dominates every other tool configuration path. If
	// true, emit "--disallowedTools '*'" and skip any AllowedTools /
	// DeniedTools / per-profile defaults; this flag exists so Plan-mode
	// guest dispatch cannot accidentally leak tool access even if a
	// future refactor forgets to zero AllowedTools. Keep this branch
	// first — do NOT hoist AllowedTools above it, and do NOT AND it with
	// other flags. See types.RunOpts.ToolsDisabled for the full
	// invariant.
	if opts.ToolsDisabled {
		args = append(args, "--disallowedTools", "*")
	} else {
		for _, tool := range opts.DeniedTools {
			args = append(args, "--disallowedTools", tool)
		}
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", opts.MaxTurns))
	}
	if !opts.ToolsDisabled && len(opts.AllowedTools) > 0 {
		for _, tool := range opts.AllowedTools {
			args = append(args, "--allowedTools", tool)
		}
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	for _, dir := range opts.AdditionalDirs {
		args = append(args, "--add-dir", dir)
	}

	return d.execute(ctx, opts.WorkspacePath, args, opts, opts.Prompt)
}

func (d *Driver) Continue(ctx context.Context, sessionID string, prompt string, opts types.ContinueOpts) (*types.RunResult, error) {
	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--resume", sessionID,
	}
	switch opts.PermissionMode {
	case "bypassPermissions":
		args = append(args, "--dangerously-skip-permissions")
	default:
		args = append(args, "--permission-mode", "dontAsk")
	}
	if len(opts.AllowedTools) > 0 {
		for _, tool := range opts.AllowedTools {
			args = append(args, "--allowedTools", tool)
		}
	}
	for _, dir := range opts.AdditionalDirs {
		args = append(args, "--add-dir", dir)
	}
	return d.execute(ctx, opts.WorkspacePath, args, types.RunOpts{
		StallTimeoutMS: opts.StallTimeoutMS,
		OnStream:       opts.OnStream,
	}, prompt)
}

func (d *Driver) Kill(pid int) error {
	// Construct a minimal cmd to pass to ProcessManager.Kill
	// In practice, the orchestrator holds the cmd reference from Run
	d.logger.Warn("Kill called directly with pid", "pid", pid)
	return fmt.Errorf("direct pid kill not supported, use context cancellation")
}

func (d *Driver) execute(ctx context.Context, workDir string, args []string, opts types.RunOpts, stdinPrompt string) (*types.RunResult, error) {
	cmd := exec.CommandContext(ctx, d.binary, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Stdin = strings.NewReader(stdinPrompt)

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

	var streamAccum strings.Builder
	onStreamHook := func(s string) {
		streamAccum.WriteString(s)
		if opts.OnStream != nil {
			opts.OnStream(s)
		}
	}

	result, scanErr := d.parseStdout(ctx, stdout, start, stallTimeout, onActivity, onStreamHook, func() {
		go func() {
			time.Sleep(postResultTimeout)
			_ = d.pm.Terminate(cmd)
		}()
	})

	waitErr := cmd.Wait()

	if result != nil {
		mergeOutputFromStreamBuffer(result, streamAccum.String())
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

// mergeOutputFromStreamBuffer fills empty RunResult.Output from streamed deltas.
// With --include-partial-messages, some Claude Code builds omit duplicate text in the
// final "result" event; the orchestrator JSON then exists only in accumulated stream chunks.
func mergeOutputFromStreamBuffer(result *types.RunResult, streamAccum string) {
	if result == nil || strings.TrimSpace(result.Output) != "" {
		return
	}
	if acc := strings.TrimSpace(streamAccum); acc != "" {
		result.Output = acc
	}
}

// parseStdout reads stream-json lines from r and extracts the result event.
// onActivity is called on each line read. onResult is called when a result event is found.
// Returns the parsed result (or nil) and any scanner error.
func (d *Driver) parseStdout(ctx context.Context, r io.Reader, start time.Time, stallTimeout time.Duration, onActivity func(), onStream func(string), onResult func()) (*types.RunResult, error) {
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

		if onStream != nil {
			if event.Type == "assistant" && event.Content != "" {
				onStream(event.Content)
			} else if chunk := StreamEventStreamingPayload(event); chunk != "" {
				onStream(chunk)
			}
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
				Output:    extractResultText(event.ResultText),
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

// extractResultText extracts response text from the result event's "result" field.
// The field may be a plain string or an array of content blocks [{type:"text",text:"..."}].
func extractResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	// Try plain string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	// Try array of content blocks.
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "")
	}

	return ""
}
