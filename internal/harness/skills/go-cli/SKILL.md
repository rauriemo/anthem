---
name: go-cli
description: >-
  Go command-line application doctrine. Use when authoring or reviewing Go CLIs,
  deciding command grammar, output contracts, exit codes, config precedence,
  thin CLI architecture, testing strategy, or agent-friendly automation safety.
metadata:
  domain: architecture
  language: go
---

# Go Command-Line Applications

Doctrine for Go CLIs that work well for humans, scripts, CI, and AI agents.

## When to use this skill

- Designing a new Go CLI or adding commands to an existing one
- Reviewing CLI command shape, flags, or operator UX
- Deciding output format (text, JSON, or both)
- Defining exit codes, config precedence, or error taxonomy
- Splitting CLI code into proper architectural boundaries
- Checking whether a CLI is safe for CI or headless agent use

## When NOT to use

- Generic Go style not specific to CLIs
- One-off scripts where long-term contracts don't matter
- Framework-specific parser syntax decisions

## North star

A good Go CLI should be:

- **Predictable** instead of clever
- **Scriptable** without fragile text scraping
- **Explicit** about mutation and resolved context
- **Thin** at the CLI layer with well-bounded internals
- **Calm** by default, diagnosable on demand

## Default package layout

```
cmd/<name>/main.go          — process startup and final exit
internal/cli/...            — command definitions, flags, surface validation
internal/app/...            — use cases and orchestration
internal/render/...         — human and JSON rendering
internal/config/...         — precedence resolution and effective config
internal/infra/...          — filesystem, HTTP, subprocesses, external systems
```

The CLI layer translates parsed input into application requests. It does not own
business logic.

## Command grammar

Default to domain-shaped commands: `noun verb` or `noun subnoun verb`.

```bash
anthem task list
anthem task dispatch --profile coder
anthem config show --effective
anthem skill install anthem://test-verifier
```

Not flag soup:

```bash
# Bad
anthem --list-tasks --filter-profile=coder --show-config --effective
```

## Output contracts

- **stdout** is for primary output. Keep it clean for pipes.
- **stderr** is for diagnostics, warnings, progress, and errors.
- Data-bearing commands support `--json` for machine-readable output.
- JSON output structure is stable across versions (additive changes only).
- Human output is the default for interactive use.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic failure |
| 2 | Usage or validation error |
| 3 | State or precondition failure |
| 4 | External dependency failure |
| 5 | Authentication or authorization failure |
| 6 | Partial success, conflict, or drift detected |

Exit codes are part of the API contract. Test them.

## Config precedence

Highest to lowest:

1. CLI flags
2. Environment variables
3. Project config file (`.anthem.yml`)
4. User config file (`~/.anthem/config.yml`)
5. Compiled defaults

Support `config show --effective` to display the resolved config with sources.

## Diagnostics

- Default: quiet (no progress, no debug)
- `--verbose`: breadcrumb-level output (what steps are happening)
- `--debug`: detailed execution (API calls, timing, resolved config)
- Never print from application services. Render at the boundary.

## Mutation safety

- Mutating commands should look mutating (`create`, `delete`, `dispatch`)
- Support `--dry-run` for destructive or expensive operations
- Print what would happen, then exit without side effects
- Never hide mutation inside a command that looks read-only

## Agent-friendly design

For commands that AI agents or CI will call:

- Always support `--json` output
- Never require interactive input (prompts, confirmations)
- Support `--yes` or `--force` to skip confirmations
- Return structured errors in JSON mode
- Make all output parseable without regex

## Review checklist

1. Does the command tree reveal the domain instead of growing into flag soup?
2. Are side effects obvious, especially for remote or destructive operations?
3. Do data commands support `--json` with stable structure?
4. Is stdout clean for pipes and stderr reserved for diagnostics?
5. Are exit codes documented, tested, and mapped from the error taxonomy?
6. Is config resolution explicit and inspectable?
7. Does the CLI layer delegate to application services, not own logic?
8. Can core workflows run non-interactively?
9. Are help text and examples realistic?
10. Are rendering, process control, and infra concerns testable without shelling?

## Common mistakes

- Printing from application services instead of rendering at the boundary
- Calling `os.Exit()` deep in command execution paths
- Reading env vars from random helpers across the codebase
- Treating JSON output as a raw dump of internal Go structs
- Designing interactive-only workflows for commands that land in CI
- Hiding mutation inside read-only-looking commands
- Adding interfaces by reflex instead of at real consumption boundaries
