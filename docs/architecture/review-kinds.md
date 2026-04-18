# Review Kinds

_Portable per-agent review contract for approval gates. Authoritative source for the contract between Anthem (guest runtime) and Prism (approval UI)._

> Status: **STUB (Stage 1).** Schema, core kinds, and the validator's diagnostic code list are pinned here. Per-kind / per-panel deep dives and the deprecation policy are filled in at Stage 5. See [Stage plan](../../../.cursor/plans/per-agent_approval_review_ux_678cd968.plan.md) for context.

---

## Why this exists

Every guest agent can produce very different kinds of artifacts: image galleries from an illustrator, short videos from an animator, quest documents from a writer, scene diffs from a world-builder. The review gate UI needs to render those artifacts in a way the author would recognize -- but we want a single contract so any consumer (Prism, Claude Code, a future IDE plugin) can render gates for a project's agents without shipping per-project code.

The answer is a **media-shape-based, project-agnostic** contract:

- An agent declares a `review:` block in its YAML frontmatter.
- The block names a **core kind** (media-shape, e.g. `image-gallery`) plus optional **panels** (composable widgets, e.g. `metadata-table`).
- Custom project-specific kinds are declared in `.prism/review-extensions.yaml` with an `extends:` fallback so consumers without that extension still render something sensible.

This doc is the contract. Anthem validates against it at guest load time; Prism pins a parity test against the core kind + panel lists.

---

## Frontmatter schema (v1)

Every field under `review:` except `kind` is optional. Anthem's parser lives in [`internal/guests/review.go`](../../internal/guests/review.go) and `internal/guests/guests.go`.

```yaml
review:
  kind: image-gallery                           # required, see "Core kinds" below
  title: "Sprites & concept art review"         # optional, header shown in the drawer
  artifact_globs:                               # optional, which files the gate surfaces
    - "Assets/_Project/Art/Generated/**/*.png"
  context_files:                                # optional, extra docs shown alongside artifacts
    - ".context/features/*/plan.md"
  show_prompt: true                             # optional, show the agent's prompt in the drawer (default: false)
  panels:                                       # optional, explicit panel ordering (see "Core panels")
    - type: viewer
    - type: metadata-table
  notification_template: "{agent_name} finished {step_title}"   # optional, guest-authored chat message when gate opens
  partial_revise: true                          # optional, override per-kind default
  coherence_hint: "Preserve established world-state…"           # optional, injected into partial-revise prompts
  schema_version: "1"
```

### Load-time validation

Every `review:` block is passed through `ValidateReviewSpec` at `ScanDirectory` time (and in the `anthem validate-agents` CLI). Diagnostics are severity-tagged:

| Severity | Effect | Emitted for |
|---|---|---|
| `error` | Spec dropped, guest still loads with `Review=nil` | missing kind, unknown kind (no extension match), missing panel `type`, unknown panel `type` |
| `warning` | Spec kept, logged, surfaced in Prism dev overlay | invalid glob, redundant `partial_revise`, unknown `notification_template` variable, persona-shaped core kind ID |
| `info` | Spec kept, advisory only | extension-kind usage (portability reminder) |

The full diagnostic code catalog is in [`ValidateReviewSpec`](../../internal/guests/review.go).

---

## Core kinds (8)

These kinds are pinned. Adding a new core kind is a PR-gated change that includes a frontend renderer, a contract test, and a doc update here. Projects can add custom kinds via `.prism/review-extensions.yaml`.

| Kind | Media shape | Typical use |
|---|---|---|
| `image-gallery` | Grid of images | 2D sprites, concept art, illustrations |
| `video-preview` | Playable video / webm | Animations, cinematics, motion tests |
| `audio-player` | Waveform + playback | SFX, music beds, voiceover |
| `document` | Rendered markdown / rich text | Narrative chapters, design docs, quests |
| `web-preview` | Iframe sandbox | HTML previews, playable mockups |
| `model-3d-preview` | Interactive 3D viewer | glTF / glb models, rigged meshes |
| `structured-data` | Schema-aware table/tree | Balance tables, configs, JSON/YAML data |
| `artifact-list` | Generic file list + metadata | Fallback; scene diffs, mixed asset bundles |

**Partial-revise support:** `image-gallery`, `video-preview`, `audio-player`, `document`, `web-preview`, `model-3d-preview`, `artifact-list` all support per-artifact revise. `structured-data` does not (it's usually one coherent file per step). Override via `partial_revise: true|false`. See [`kindsSupportingPartialRevise`](../../internal/guests/review.go).

---

## Core panels (6)

Panels are composable widgets that render inside a kind. Adding a new panel is also a PR-gated change.

| Panel | Purpose |
|---|---|
| `viewer` | The kind's primary content viewer (image, video, doc, 3D) |
| `metadata-table` | Key/value grid of per-artifact metadata |
| `tree` | Hierarchical data (scene graph, rig skeleton, JSON tree) |
| `list` | Bullet/numbered list of items, optionally with per-item fields |
| `diff` | Side-by-side or unified diff |
| `markdown` | Rendered markdown (release notes, context docs) |

Panels can take a `source` glob and, for `list`/`metadata-table`, per-item field projections (`item_fields: [id, title, objectives]`).

---

## Extension kinds (per-project)

A project can declare custom kinds in `<projectRoot>/.prism/review-extensions.yaml`:

```yaml
schema_version: "1"
kinds:
  - id: beatmap-preview
    extends: structured-data
    description: Beatmap timeline + audio sync preview
  - id: dialogue-graph
    extends: document
    description: Interactive dialogue tree with branching preview
```

Consumers that don't implement the extension kind MUST fall back to the `extends:` kind so gates still render. Anthem loads the manifest in `LoadExtensionManifest`; the validator uses it to suppress `review.kind.unknown` errors (and emits an `info` diagnostic instead).

---

## Authoring-time CLI

```
anthem validate-agents --dir agents
```

Loads every agent, runs `ValidateReviewSpec` against the project's extension manifest, and exits non-zero on any `error` diagnostic. Intended for CI. Warnings and info diagnostics are printed but do not fail the run.

---

## Pinned diagnostic codes

For stability across the Anthem / Prism boundary, the `code` field on `ReviewDiagnostic` is pinned. Every code is of the form `review.<area>.<problem>`:

- `review.kind.missing`
- `review.kind.unknown`
- `review.kind.extension` _(info only)_
- `review.kind.persona_shaped` _(warning only)_
- `review.panels.type.missing`
- `review.panels.type.unknown`
- `review.artifact_globs.invalid`
- `review.partial_revise.redundant`
- `review.notification_template.unknown_var`

Prism's diagnostic overlay groups by code; tests pin the string.

---

## Still to land (Stage 5 fill)

- Per-kind deep dive: expected artifact patterns, typical panel layouts, partial-revise semantics, coherence caveats.
- Per-panel deep dive: config options, data-shape expectations.
- Gate event payload contract (Phase 2 adds fields here).
- Selective-revise wire protocol and preserve-and-replace semantics (Phase 5a).
- Deep-link URL contract and bootstrap resolver behavior (Phase 5b.5a).
- Authoring cookbook: 5 complete real-world examples.
- Anti-patterns: what not to declare (persona-shaped core kinds, unbounded globs, notification spam).
- Governance: PR template, stewardship, deprecation policy.
