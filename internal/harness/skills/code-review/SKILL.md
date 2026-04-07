---
name: code-review
description: >-
  Structured code review across security, performance, code quality, and test
  coverage. Produces a severity-tiered report with actionable findings. Use
  when reviewing code changes, pull requests, auditing code quality, or when
  a review cycle needs a structured assessment.
metadata:
  domain: quality
---

# Code Review

Structured, repeatable code review covering four categories with severity-tiered
output.

## When to use this skill

- When reviewing code changes or pull requests
- When auditing code quality of a module or package
- When a review cycle needs a structured pass before approval
- When asked to check code for security, performance, or quality issues

## When NOT to use

- When running tests (use test-verifier)
- When verifying an issue is resolved (use gh-issue-verifier)
- When the task is writing new code, not reviewing existing code

## Review categories

### 1. Security
- SQL injection, command injection, XSS
- Path traversal and directory escape
- Hardcoded secrets, credentials, API keys
- Improper authentication or authorization checks
- Insecure deserialization or input handling
- Missing input validation or sanitization
- Unsafe use of `os/exec`, `unsafe`, or `reflect`

### 2. Performance
- N+1 queries or redundant database calls
- Missing caching opportunities
- Unnecessary allocations in hot paths
- Blocking operations in concurrent code
- Unbounded goroutine spawning
- Large copies where pointers would suffice
- Missing context cancellation propagation

### 3. Code quality
- Code duplication (DRY violations)
- Functions doing too much (SRP violations)
- Deep nesting or complex conditionals
- Magic numbers and strings
- Poor naming that obscures intent
- Missing or swallowed error handling
- Incomplete type coverage or interface compliance
- Dead code or unused exports

### 4. Test coverage
- Missing tests for new or changed code
- Tests that assert implementation details instead of behavior
- Flaky test patterns (timing, ordering, shared state)
- Missing edge cases (nil, empty, boundary values)
- Mocked dependencies that hide real failures
- Test files that don't follow project conventions

## Procedure

1. **Scope the review.** Identify the changed files or the target module. Use `git diff` for PR reviews or read the target package for audits.
2. **Security pass first.** Scan for the security checklist items. Flag anything critical immediately.
3. **Performance pass.** Look for the performance patterns, especially in hot paths or frequently-called code.
4. **Quality pass.** Check structural quality, naming, error handling, and design.
5. **Test pass.** Verify test coverage for changed code. Note missing tests.
6. **Produce the report.** Use the output contract below.

## Output contract

```markdown
## Code Review Summary

**Scope:** <files or packages reviewed>
**Overall:** APPROVE / REQUEST CHANGES / NEEDS DISCUSSION

### Critical (Must Fix)
- **[file:line]** <issue description>
  - **Why:** <explanation of risk or impact>
  - **Fix:** <suggested fix>

### Suggestions (Should Consider)
- **[file:line]** <issue description>
  - **Why:** <explanation>
  - **Fix:** <suggested approach>

### Nits (Optional)
- **[file:line]** <minor suggestion>

### What's Good
- <positive feedback on good patterns, clean abstractions, thorough tests>

### Test Gaps
- <list of untested code paths or missing edge cases>
```

## Review checklist

Quick reference for the minimum checks:

- [ ] No hardcoded secrets or credentials
- [ ] Input validation present for external data
- [ ] Error handling complete (no swallowed errors)
- [ ] Types and interfaces well-defined
- [ ] Tests added for new code
- [ ] No obvious performance issues in hot paths
- [ ] Code is readable without excessive comments
- [ ] Breaking changes documented
- [ ] Concurrency safety (mutexes, channels used correctly)
- [ ] Context propagation and cancellation handled

## Composition with other skills

| Skill | How to use together |
|---|---|
| `test-verifier` | Run tests to validate findings; attach results to review |
| `gh-issue-verifier` | Verify the reviewed code actually satisfies the target issue |
| `commit-hygiene` | Check commit structure and message quality as part of review |
| `tdd-classicist` | Reference for test quality assessment in the test pass |
