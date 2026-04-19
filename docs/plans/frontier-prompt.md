# Claude Code CLI Prompt for Frontier Implementation

## How to run

From the Anthem project root:

```bash
claude --dangerously-skip-permissions
```

Then paste the prompt below. The `--dangerously-skip-permissions` flag skips all file edit and command execution confirmations -- including `gh` / `curl` / network tools. Do NOT assume it is scoped to local files: Claude can and will shell out to any installed CLI with no prompt. Keep remote-mutating actions out of prompts run under this flag. (Orchestrator-initiated tracker mutations are separately gated by mode in `orchestrator.executeActions` via `IsLoopOnlyAction`; Chat/Plan/Execute cannot create GitHub issues.)

After each tier completes, exit and re-enter to keep the context window fresh.

---

## Prompt (copy everything below this line)

You are implementing Anthem's Phase 4 Frontier Implementation. Read `CLAUDE.md` for project context and `docs/plans/frontier-implementation.md` for the full working checklist.

**Your task**: Work through the frontier checklist in order (1a -> 1b -> 1c -> 2a -> 2c -> 2b -> 2d -> 2e -> 3c -> 3b -> 3a). For each item:

1. Read the relevant source files listed in the checklist
2. Implement the changes described
3. Write or update table-driven tests using existing mock patterns (MockRunner, MockEventBus, MockTracker, MockWorkspaceManager in the test files)
4. Run `go test ./...` to verify
5. Run `go vet ./...` to check for issues
6. Mark the checkbox `[x]` in `docs/plans/frontier-implementation.md`
7. Commit with a descriptive message: `frontier: <tier><id> -- <short description>`

**Important rules**:
- Read `CLAUDE.md` thoroughly before starting -- it has all locked-in design decisions and coding standards
- Use `log/slog` for logging, `fmt.Errorf("context: %w", err)` for error wrapping
- No unnecessary comments -- only comment non-obvious intent
- Table-driven tests, interface-based mocks, no mocking frameworks
- Do not modify constraint definitions or guardrail systems
- Preserve backward compatibility -- all existing tests must continue to pass
- Run `go test ./...` after EVERY change, not just at the end

Start with **Task 1a: Fix audit log gaps**. Read `internal/orchestrator/orchestrator.go` and `internal/audit/audit.go` first, then implement the fixes described in the checklist.
