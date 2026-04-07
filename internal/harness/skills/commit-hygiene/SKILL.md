---
name: commit-hygiene
description: >-
  Commit hygiene and self-contained pull requests. Conventional commit format,
  separation of concerns per commit, and PR quality standards. Use when
  committing code, creating pull requests, or reviewing commit history for
  cleanliness.
metadata:
  domain: workflow
---

# Commit Hygiene

## When to use this skill

- When committing code changes
- When creating or updating pull requests
- When reviewing commit history for cleanliness
- When deciding how to split changes across commits
- Before pushing to ensure commit quality

## When NOT to use

- When deciding what to implement (use plan mode)
- When writing tests (use tdd-classicist)
- When reviewing code quality (use code-review)

## Conventional commits

Every commit message must follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <description>

[optional body]
```

Allowed types:

| Type | Purpose |
|------|---------|
| `feat` | New feature or capability |
| `fix` | Bug fix |
| `refactor` | Code restructuring without behavior change |
| `perf` | Performance improvement |
| `test` | Adding or updating tests |
| `docs` | Documentation only |
| `chore` | Build, tooling, config changes |
| `ci` | CI/CD pipeline changes |
| `build` | Build system or dependency changes |
| `style` | Formatting, whitespace (no logic change) |

## Separation of concerns

Never mix these categories in a single commit:

| Category | Files | Commit type |
|----------|-------|-------------|
| **Production code** | `*.go` in `internal/`, `cmd/` (excluding `*_test.go`) | `feat`, `fix`, `refactor`, `perf` |
| **Test code** | `*_test.go`, `testdata/`, test fixtures | `test` |
| **Documentation** | `*.md`, `docs/`, `CLAUDE.md`, `README*` | `docs` |
| **Config/tooling** | `Makefile`, `.github/`, `.golangci.yml`, `go.mod` | `chore`, `ci`, `build` |

### Rules

1. A commit touching production `.go` files must NOT include test or doc files.
2. A commit touching test files must NOT include production code or doc files.
3. A docs-only commit must use `docs:` type and contain no code changes.
4. Config/tooling files use `chore` or `ci` and may stand alone or accompany production code if directly related.

### Example sequence

```
feat(orchestrator): add profile-based MCP server resolution
test(orchestrator): add resolveProfile unit tests
docs: update README with harness architecture
```

### When in doubt

If a change requires both production and test updates (e.g., TDD workflow),
create two commits: one for the production code, one for the tests. Mention the
relationship in the commit body if helpful.

## Commit message quality

### Good commit messages

- Start with a verb in imperative mood: "add", "fix", "remove", "refactor"
- Focus on WHY, not WHAT (the diff shows what changed)
- Keep the subject line under 72 characters
- Use the body for context, trade-offs, or issue references

```
feat(harness): add workspace skill preparation for Claude Code discovery

Skills are copied to .claude/skills/ before agent launch so Claude Code
auto-discovers them via three-level progressive loading. Built-in skills
use anthem:// prefix; project-local skills are already in the workspace.
```

### Bad commit messages

```
fix stuff
update code
WIP
changes
feat: implement the thing we discussed
```

## Pull request quality

### Focused scope

- Keep PRs under ~500 lines when possible
- Separate refactoring from behavioral changes
- One logical change per PR

### Structure

A PR should include:

1. **Why:** One sentence explaining why this change exists
2. **What:** Summary of what changed (file inventory for large PRs)
3. **Test evidence:** What was tested and the results
4. **Safe to merge:** Why this is safe (scope containment, no regressions, etc.)

### Test evidence

Every PR should include test results or explain why tests aren't applicable:

```markdown
### Test Evidence
- `go test ./...` — 142 pass, 0 fail
- `go test -race ./...` — clean
- New tests: TestResolveProfile_MCPRefsResolved, TestFocusToProfile_Mapping
```

## Composition with other skills

| Skill | How to use together |
|---|---|
| `test-verifier` | Run verification before committing; include results in PR |
| `code-review` | Review checks commit structure and message quality |
| `gh-issue-verifier` | Ensure commits reference the issue they resolve |
