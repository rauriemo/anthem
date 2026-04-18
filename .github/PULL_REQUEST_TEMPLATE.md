<!--
Thanks for the PR. Keep the description focused on *why* the change is being
made; a reviewer can always read the diff for *what* changed.
-->

## Summary

<!-- 1-3 sentences. What's the user-visible or architectural shift? -->

## Test plan

<!-- Commands you ran + what you checked manually, if anything. -->

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ]
- [ ]

## Review-kind changes (if applicable)

<!--
Delete this section if the PR does not touch the review-kind contract. Fill it
out if you added, renamed, or removed a core review kind, a panel type, or the
ValidateReviewSpec rules. See docs/architecture/review-kinds.md for the full
contract.
-->

- [ ] Does this PR add, rename, or remove a **core review kind**?
  - If yes: update `docs/architecture/review-kinds.md` (authoritative table), add the entry to `kindRegistry` in Prism, add parity + identity tests (`TestCoreKindIdentity_NoPersonaLeak`, `TestCoreKindIdentity_MatchesReviewKindsDoc`, `reviewRegistry.test.tsx > governance`), and in the PR description justify why this kind cannot be expressed by composing existing panels on an existing kind.
- [ ] Does this PR add a **new panel type**?
  - If yes: add to `panelRegistry` in Prism, document in `review-kinds.md`, add a panel test file.
- [ ] Does this PR change a **diagnostic code** or introduce a new one?
  - If yes: update the pinned code list in `review-kinds.md` and the catalog in `ValidateReviewSpec`.

Core-kind additions need a review-kinds steward sign-off. See the "Governance" section of `docs/architecture/review-kinds.md`.
