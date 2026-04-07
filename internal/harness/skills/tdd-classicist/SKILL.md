---
name: tdd-classicist
description: >-
  Classicist TDD methodology — test-first Red-Green-Refactor cycle, Meszaros
  test-double taxonomy, tiered test pyramid, and assertion policies. Use when
  writing tests, choosing test doubles, deciding test tier, reviewing test
  quality, or fixing bugs with a test-first approach. Language-agnostic.
metadata:
  domain: testing
  methodology: tdd
---

# TDD — Classicist

Classicist by default, pragmatic about doubles, strict about test purpose and
boundaries. This skill encodes a language-agnostic testing methodology.

## When to use this skill

- Deciding which test tier a test belongs to (unit / integration / functional / E2E)
- Choosing a test double type (dummy / fake / stub / spy / mock)
- Guidance on what to assert (state vs behavior, contract-shaped assertions)
- Following the TDD cycle (Red-Green-Refactor)
- Reviewing tests for quality, brittleness, or mock overuse
- Writing a test for a bug fix (reproduce first, then fix)

## When NOT to use

- Running tests or collecting coverage (use test-verifier)
- Deciding file naming or directory layout (follow project conventions)
- Configuring test runners or CI pipelines

## The Red-Green-Refactor cycle

1. **Red:** Write a failing test that describes the desired behavior. The test must fail for the right reason — not because of a syntax error or missing import.
2. **Green:** Write the minimum production code to make the test pass. No more.
3. **Refactor:** Clean up both test and production code while keeping all tests green. Remove duplication, improve naming, extract helpers.

Rules:
- Never write production code without a failing test
- Never write more test than is sufficient to fail
- Never write more production code than is sufficient to pass
- Refactor only when all tests are green

## Test tier taxonomy

| Tier | Purpose | Scope | Speed | Dependencies |
|---|---|---|---|---|
| **Unit** | Verify a single function or method | One package | Fast (ms) | No I/O, no network, no disk |
| **Integration** | Verify cross-package or external interactions | Multiple packages or services | Medium (s) | Database, filesystem, APIs |
| **Functional** | Verify user-facing behavior end-to-end | Full request/response cycle | Slow (s) | Running application |
| **E2E** | Verify deployed system behavior | Full deployed stack | Slowest (min) | Live infrastructure |

### Choosing a tier

- Can you test it with no I/O? → **Unit**
- Does it need a database, filesystem, or API? → **Integration**
- Does it test a complete user workflow? → **Functional**
- Does it need a deployed environment? → **E2E**

Default to the lowest tier that can meaningfully verify the behavior.

## Test double taxonomy (Meszaros)

| Double | Purpose | Preference |
|---|---|---|
| **Dummy** | Fills a parameter slot; never actually used | Always fine |
| **Stub** | Returns canned answers to calls | Preferred |
| **Fake** | Working implementation with shortcuts (in-memory DB) | Preferred |
| **Spy** | Records calls for later verification | Use when state isn't observable |
| **Mock** | Pre-programmed expectations that verify behavior | Last resort |

### Choosing a double

Preference order: **real object > stub > fake > spy > mock**

- Can you use the real object? → Use it (classicist default)
- Is the real object slow or non-deterministic? → **Stub** the interface
- Do you need a working but simplified version? → **Fake** it
- Do you need to verify a call was made but can't observe state? → **Spy**
- Is the interaction the ONLY thing that matters? → **Mock** (justify it)

Rule: If you find yourself mocking more than 2 dependencies, the unit under test probably has too many responsibilities. Refactor the design before adding more mocks.

## Assertion policies

### Assert state, not implementation
```
# Good: Assert the result
assert result.total == 150

# Bad: Assert internal method was called
assert calculator.add.was_called_with(100, 50)
```

### Assert contracts, not internals
```
# Good: Assert the API contract
assert response.status == 200
assert response.body.id is not None

# Bad: Assert internal query structure
assert db.last_query == "SELECT * FROM users WHERE id = 1"
```

### One logical assertion per test
A test should verify one behavior. Multiple `assert` statements are fine if they all verify aspects of the same behavior. Multiple unrelated assertions in one test make failures ambiguous.

## Suite health checklist

When reviewing a test suite, check:

- [ ] Tests are independent (no shared mutable state, no ordering dependency)
- [ ] Tests are deterministic (no flakiness from timing, randomness, or concurrency)
- [ ] Test names describe behavior, not implementation (`TestRejectsExpiredTokens` not `TestValidate`)
- [ ] No test logic (no `if/else` in tests; use table-driven tests for variants)
- [ ] Failures are diagnostic (error messages explain what was expected vs actual)
- [ ] Mock count is low (< 2 mocks per test on average)
- [ ] No tests of private implementation details
- [ ] Fast tests run first (unit before integration)

## Bug fix procedure

1. **Reproduce:** Write a failing test that demonstrates the bug
2. **Verify the test fails:** Confirm it fails for the right reason
3. **Fix:** Write the minimum code to make the test pass
4. **Verify no regressions:** Run the full test suite
5. **Refactor:** Clean up if needed, keeping tests green

Never fix a bug without a regression test. The test is the proof.

## Composition with other skills

| Skill | How to use together |
|---|---|
| `test-verifier` | This skill guides HOW to test; test-verifier handles WHAT to run |
| `code-review` | Reference for the test quality assessment in reviews |
| `gh-issue-verifier` | Tests serve as implementation evidence for verification |
