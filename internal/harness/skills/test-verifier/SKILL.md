---
name: test-verifier
description: >-
  Verify working-tree changes against a multi-tier test pyramid for Go projects.
  Run the right tests for changed files, collect coverage, detect race conditions,
  and return a structured verification report. Use when the agent modifies
  production code, the user asks to run tests or check coverage, or a review
  cycle needs test evidence.
metadata:
  domain: testing
  language: go
---

# Test Verifier — Multi-Tier Go Test Runner

## When to use this skill

- After modifying production source files
- When the user asks to "run tests", "verify changes", "check coverage"
- When a review cycle needs "test evidence" before approval
- Before creating a commit or PR, to confirm nothing is broken
- When explicitly asked to verify test coverage

## When NOT to use

- When writing new tests (use tdd-classicist instead)
- When reviewing test quality without running them (use code-review instead)
- When the task is pure documentation or config changes with no testable code

## Tier selection logic

Map changed file paths to the tiers that should run:

| Changed path pattern | Tiers to run |
|---|---|
| `internal/*/` (non-test `.go` files) | unit |
| `internal/*/` + test files changed | unit + race |
| `cmd/*/` | unit + integration |
| Database, API, or external service code | unit + integration |
| Concurrency-related code (`sync.`, `chan`, `go func`) | unit + race |
| Performance-sensitive paths | unit + benchmark |
| Multiple packages touched | unit + race (minimum); + integration if infra changed |

### The Go test pyramid

| Tier | What it tests | Command | When to run |
|---|---|---|---|
| Unit | Package-level logic in isolation | `go test ./...` | Always |
| Race | Concurrency safety | `go test -race ./...` | When concurrency code changes |
| Integration | Cross-package and external deps | `go test -tags=integration ./...` | When infra/API code changes |
| Benchmark | Performance regressions | `go test -bench=. -benchmem ./...` | When hot-path code changes |
| Fuzz | Input edge cases | `go test -fuzz=FuzzXxx -fuzztime=30s` | When parsing or input handling changes |

## Procedure

1. **Identify changed files.** Run `git diff --name-only HEAD` or inspect the working tree.
2. **Map to tiers.** Use the tier selection table above.
3. **Run minimum tiers first.** Always start with unit. Add race/integration/benchmark only when the changed paths warrant it.
4. **Collect coverage.** Use `go test -coverprofile=coverage.out -covermode=atomic ./...` for the unit tier. Use `go tool cover -func=coverage.out` for the summary.
5. **Parse results.** Extract pass/fail counts, coverage percentages, and any failure details.
6. **Report.** Use the output contract below.

### Running tests

```bash
# Unit tests with coverage
go test -coverprofile=coverage.out -covermode=atomic ./...

# Coverage summary
go tool cover -func=coverage.out

# Race detection
go test -race ./...

# Integration tests (if tagged)
go test -tags=integration ./...

# Benchmarks
go test -bench=. -benchmem -run=^$ ./path/to/package

# Fuzz (targeted)
go test -fuzz=FuzzFunctionName -fuzztime=30s ./path/to/package
```

### Reading coverage

```bash
# Function-level coverage summary
go tool cover -func=coverage.out

# Package-level summary (percentage per package)
go tool cover -func=coverage.out | grep total

# HTML report (for visual inspection)
go tool cover -html=coverage.out -o coverage.html
```

## Output contract

When reporting verification results, use this structured format:

```markdown
## Verification Report

- **Changed files:** file1.go, file2.go
- **Tiers run:** unit, race
- **Skipped tiers:** integration (no infra changes), benchmark (no hot-path changes)

### Results
| Tier | Passed | Failed | Duration |
|------|--------|--------|----------|
| unit | 47 | 0 | 4.2s |
| race | 47 | 0 | 8.1s |

### Failures (if any)
1. **unit** `internal/orchestrator` `TestResolveProfile_MCPRefsResolved`
   > Expected .mcp.json to contain "semgrep" entry
   > Got: empty mcpServers map

### Coverage
| Metric | Value |
|--------|-------|
| Total line coverage | 72.3% |
| Changed-file coverage | 85.1% |
| Uncovered functions | `handleEdgeCase`, `parseConfig` |

### Verdict
- PASS / FAIL / PARTIAL (tests pass but coverage below threshold)
```

## Coverage thresholds

Default thresholds (override via project config):

| Metric | Minimum |
|--------|---------|
| Total line coverage | 60% |
| Changed-file coverage | 80% |
| No regressions | Coverage must not decrease from baseline |

## Composition with other skills

| Skill | How to use together |
|---|---|
| `commit-hygiene` | Use the verification report as go/no-go before committing |
| `code-review` | Attach the verification report to the review |
| `tdd-classicist` | Consult for test design decisions; this skill handles execution |
| `gh-issue-verifier` | Test results serve as implementation evidence |
