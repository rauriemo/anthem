---
name: gh-issue-verifier
description: >-
  Verify whether a GitHub issue is implemented in the current codebase by
  collecting evidence from the GitHub issue, git history, code, and tests,
  then producing a structured report with verdict, proof, gaps, and confidence.
  Use when the agent needs to confirm an issue is resolved, a PR satisfies an
  issue, or a branch truly implements what was requested.
metadata:
  domain: verification
  tools: [gh, git, rg]
---

# GH Issue Verifier

## When to use this skill

- To determine whether a specific GitHub issue has been implemented
- To verify a branch or PR satisfies an issue's requirements
- To produce evidence-based verification before closing an issue
- When a review cycle needs proof that acceptance criteria are met
- To identify gaps between what was requested and what was delivered

## When NOT to use

- For fixing code or implementing features (use coder profile)
- For implementation planning (use plan mode)
- For generic code review without a specific issue target (use code-review)
- For writing PR descriptions (do that directly)

## Inputs required

Minimum:
- Repository context (usually inferred from workspace)
- Issue number or URL

Optional comparison target:
- Current branch name
- PR number or URL
- Explicit commit range

## Procedure

1. **Fetch the issue.** Use `gh issue view <number> --json title,body,labels,state,comments` to get the full issue context.

2. **Extract acceptance criteria.** Parse the issue body for:
   - Explicit acceptance criteria or checklists
   - Behavioral requirements ("should", "must", "when X then Y")
   - Referenced files, APIs, or components
   - If criteria are vague, infer them from the issue description and note the inference

3. **Gather implementation evidence.** Run these in parallel:
   ```bash
   # Commits mentioning the issue
   git log --all --oneline --grep="#<number>"

   # Files changed in the relevant branch/PR
   git diff main..HEAD --name-only

   # Search for key terms from the issue
   rg "relevant_function_name" --type go
   rg "relevant_api_endpoint" --type go
   ```

4. **Inspect candidate files.** Read at most 5 candidate implementation files first. Look for:
   - Functions or types that match the issue requirements
   - API endpoints or handlers mentioned in the issue
   - Configuration changes requested

5. **Check test coverage.** Read at most 3 candidate test files. Verify:
   - Tests exist for the implemented behavior
   - Edge cases mentioned in the issue are covered
   - Tests actually pass (`go test ./path/to/package`)

6. **Check documentation.** If the issue requested doc changes:
   - Verify README, CLAUDE.md, or other docs were updated
   - Verify inline code comments match new behavior

7. **Produce the verdict.** Use the evidence ranking below.

## Evidence ranking

| Evidence type | Strength | Example |
|---|---|---|
| Passing test that exercises the requirement | Strong | `TestHandleAuth_RejectsExpiredTokens` passes |
| Code implementing the exact behavior described | Strong | New handler function matching issue spec |
| Commit message referencing the issue | Medium | `fix: resolve #42 — add token expiry check` |
| Related code changes without direct test | Weak | Modified auth handler, no new tests |
| Only documentation changes | Weak | README updated but no code change |
| No matching code or tests found | None | — |

## Verdict rubric

| Verdict | Criteria |
|---|---|
| **RESOLVED** | Strong evidence for all acceptance criteria. Tests pass. No gaps. |
| **PARTIALLY RESOLVED** | Some criteria met with strong evidence, others have gaps or weak evidence. |
| **NOT RESOLVED** | No strong evidence for core requirements. Missing implementation or tests. |
| **INCONCLUSIVE** | Evidence is ambiguous, contradictory, or access-limited. |

## Output contract

```markdown
## Issue Verification Report

**Issue:** #<number> — <title>
**Branch/PR:** <branch or PR number>
**Verdict:** RESOLVED / PARTIALLY RESOLVED / NOT RESOLVED / INCONCLUSIVE
**Confidence:** High / Medium / Low

### Acceptance Criteria
| # | Criterion | Evidence | Status |
|---|-----------|----------|--------|
| 1 | <requirement> | <file:line or test name> | Met / Gap / Unclear |
| 2 | <requirement> | <evidence> | Met / Gap / Unclear |

### Implementation Evidence
- **Files changed:** list of relevant files
- **Key commits:** commit hashes and messages
- **Tests:** test names and pass/fail status

### Gaps
- <missing requirement or weak evidence area>

### Residual Risks
- <anything that could regress or was only partially addressed>
```

## Evidence budget

- Start with `gh` and `git` metadata, not raw code reading
- Inspect at most 5 candidate implementation files first
- Inspect at most 3 candidate test files first
- Prefer `INCONCLUSIVE` over expanding the search without a strong reason
- Do not suggest code fixes — this skill is strictly observational

## Composition with other skills

| Skill | How to use together |
|---|---|
| `test-verifier` | Run tests to produce evidence for this verification |
| `code-review` | Review covers quality; this skill covers requirement satisfaction |
| `commit-hygiene` | Check commit messages reference the issue correctly |
