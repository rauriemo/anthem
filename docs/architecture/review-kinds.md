# Review Kinds

_Portable per-agent review contract for approval gates. Authoritative source for the contract between Anthem (guest runtime) and Prism (approval UI)._

> Status: **v1 shipped.** Stages 1-5 landed; this document is the pinned contract. Cross-repo governance tests assert this doc and the runtime registries agree.

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

### Per-kind deep dive

#### `image-gallery`
- **Expected artifacts:** one image per file. Typical extensions: `.png`, `.jpg`, `.webp`. Sprite-sheets go here too (the panel layer handles the sheet shape).
- **Typical panels:** `viewer` (default grid), optional `metadata-table` per tile (width/height/frames/palette).
- **Selective-revise UX:** per-tile rerun icon; optional per-tile note. Flagging ≥ 1 tile turns the primary action bar into "Rerun N selected" with a secondary "Rerun all with this feedback" escape hatch.
- **Coherence caveats:** repeated partial revises of a set can drift style. Use the per-agent `coherence_hint` (e.g. "Match palette/lighting of preserved tiles") to anchor style.

#### `video-preview`
- **Expected artifacts:** playable video files (`.mp4`, `.webm`). Related spritesheets (`*.spritesheet.json`) can be surfaced in a sibling `list` panel.
- **Typical panels:** `viewer` (player), optional `list` for spritesheet frames, optional `metadata-table` (duration_ms, fps, codec).
- **Selective-revise UX:** card-level rerun icon next to each video title; per-card note.

#### `audio-player`
- **Expected artifacts:** `.wav`, `.mp3`, `.ogg`. SFX banks should be one file per cue.
- **Typical panels:** `viewer` (waveform + transport), `metadata-table` (duration_ms, sample_rate, channels).
- **Selective-revise UX:** row-level rerun icon.

#### `document`
- **Expected artifacts:** `.md` primarily; also `.txt` and simple HTML when authored. A chapter = one file.
- **Typical panels:** `markdown` (rendered), `tree` (for nested story roots or quest chains), `list` (flat chapter index).
- **Selective-revise UX:** chapter-level rerun icon in the sidebar; main pane stays on the currently-selected file while flagging.

#### `web-preview`
- **Expected artifacts:** `.html` entry point; relative assets (`Assets/...`) auto-rewritten through `fileUrl(slug, path)` so iframe `src` resolves through the Prism file server.
- **Typical panels:** `viewer` (iframe with sandbox attrs matching `HtmlView`). `markdown` alongside for context.
- **Selective-revise UX:** tab-level rerun icon if multiple HTML artifacts; flag state persists across tab switches.

#### `model-3d-preview`
- **Expected artifacts:** `.glb` / `.gltf`. A rig/skeleton JSON can be surfaced in a sibling `tree` panel.
- **Typical panels:** `viewer` (`<model-viewer>`), `tree` for skeleton, `metadata-table` (tri_count, bone_count).
- **Selective-revise UX:** card-level rerun; does not conflict with the viewer's own orbit controls.

#### `structured-data`
- **Expected artifacts:** JSON or YAML (tables, configs, balance sheets).
- **Typical panels:** `viewer` (table/tree), optional `diff` against a prior snapshot.
- **Selective-revise UX:** **none**. Semantic coherence of structured data is file-level, not item-level. Flag state is ignored; `partial_revise: true` on this kind emits a warning.

#### `artifact-list`
- **Expected artifacts:** anything. Fallback kind.
- **Typical panels:** `list` (file path, size, last-modified).
- **Selective-revise UX:** row-level rerun icon on rows where `ArtifactID` is stable.

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

### Per-panel data shape

| Panel | Required fields | Optional fields | Data shape |
|---|---|---|---|
| `viewer` | _(none — kind-driven)_ | `source` | The view chooses its viewer based on `review.kind` |
| `metadata-table` | `source` glob | `item_fields` | key/value pairs from YAML frontmatter or sibling `.meta.yaml` |
| `tree` | `source` path | — | JSON/YAML with `children: []` arrays |
| `list` | `source` glob | `item_fields` | either file list or array-of-objects |
| `diff` | `source` (two paths or glob of pairs) | — | unified diff or pair of files |
| `markdown` | `source` path | — | rendered as GFM |

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

GitHub Actions snippet:

```yaml
- name: Validate agent review specs
  run: anthem validate-agents --dir agents/
```

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

## Gate event payload contract

When a plan run reaches an approval gate, the runner emits two events **in order**:

1. `execution.gate_notification` — a **real guest-authored chat message** announcing the gate. Attribution is the finishing agent (not the orchestrator), with `VoiceRequest: true` and a `review_link` pointing at the chain drawer. Consumers that can speak text (Voice Gateway) speak in the guest's voice.
2. `execution.gate_opened` — the control event that flips the chain row to OPEN and carries the full review spec.

`gate_opened` JSON shape (only fields relevant to the review contract shown):

```json
{
  "gate_id": "g-42",
  "prompt": "Review the sprites",
  "allowed_actions": ["approve", "revise", "abort"],
  "artifacts": [
    { "step_id": "s1", "path": "Assets/...", "kind": "image/png", "artifact_id": "art-<hash>" }
  ],
  "agent_id": "miyazaki",
  "agent_name": "Miyazaki",
  "step_id": "s1",
  "plan_id": "plan-42",
  "review_link": "/chain?plan=plan-42&gate=g-42",
  "review": {
    "kind": "image-gallery",
    "panels": [...],
    "context_files": [...]
  },
  "review_diagnostics": [
    { "severity": "warning", "code": "review.artifact_globs.invalid", "message": "..." }
  ]
}
```

`gate_notification` payload:

```json
{
  "gate_id": "g-42",
  "step_id": "s1",
  "plan_id": "plan-42",
  "agent_id": "miyazaki",
  "agent_name": "Miyazaki",
  "voice_id": "...",
  "voice_model": "eleven_flash_v2_5",
  "text": "I've finished \"Sprites\" and need your review (10 artifact(s)).",
  "review_link": "/chain?plan=plan-42&gate=g-42",
  "voice_request": true,
  "cause": "gate_opened"
}
```

Back-compat: the legacy `[gate:approve|revise|abort]` chat-text path is preserved. Channels without structured frames (e.g. Slack, email) still work through regex parsing; the structured-frame path is purely additive and targets by `gate_id`.

---

## Selective revise wire protocol

Prism frontend emits a single `gate_action` WebSocket frame. Anthem parses it through the prism adapter:

```json
{
  "type": "gate_action",
  "gate_id": "g-42",
  "action": "revise",
  "revision_text": "Make 3 and 7 more dramatic",
  "flagged_artifacts": [
    { "artifact_id": "art-3", "note": "more contrast" },
    { "artifact_id": "art-7" }
  ],
  "thread": "thread-uuid"
}
```

Preserve-and-replace semantics (PlanRunner, Anthem):

1. Gate resolves with `FlaggedArtifacts` non-empty → `applyRevise` computes **preserve** (everything not flagged) and **regenerate** (flagged). Preserve is stashed under the step ID.
2. Step description is rewritten with a structured `ReviseBlock`:
   - `## Preserve these artifacts` — bullet list of preserved paths.
   - `## Regenerate these artifacts` — bullet list with per-item `note` hints.
   - `## Coherence hint` — per-agent `review.coherence_hint` (or a sensible default per kind).
3. Agent re-runs and collects fresh artifacts.
4. `mergePreservedWithCollected` unions preserve + newly produced items. On path conflicts, new wins. Newly produced items are flagged `UpdatedInLastRevise: true` so the UI can badge them.
5. Next `gate_opened` fires with the merged set.

Partial-revise streak tracking:
- `ConsecutivePartialRevises[stepID]` increments per partial revise.
- `clearPartialReviseState` resets on `GateApprove` or `GateAbort`.
- Streak is mirrored on the frontend (`ExecutionStep.partial_revise_streak`) via `recordReviseIntent` called at WS-send time. When it hits `COHERENCE_HINT_THRESHOLD` (3), the ReviewDrawer surfaces a dismissable "consider a full revise" banner with a pointer to the "Rerun all with this feedback" escape hatch.

Opt-out:
- Per-agent: `partial_revise: false` in the agent's `review:` block. Incoming `flagged_artifacts` is logged and ignored; revise runs as a whole-step revise.
- Per-kind: `structured-data` is always treated as opt-out.

---

## Deep-link URL contract

Every approval gate has a stable, shareable URL. Shape:

```
/chain?plan=<plan_id>&gate=<gate_id>
```

This URL is the **single source of navigation truth** for Prism: the chain view's per-gate "Review & Approve" anchor and the chat notification bubble's link use _exactly the same URL_. Cross-view parity is pinned by `ExecutionPlanView.linkParityWithChat.test.tsx`.

Resolver contract:
- **Fresh load** — bootstrap resolver reads `?plan=&gate=` on mount. If the plan snapshot isn't in the store yet, a "Loading plan…" state shows in the drawer slot; when the snapshot arrives, the drawer mounts for the target gate and the chain row scrolls + highlights.
- **Missing gate** — when `plan_id` or `gate_id` don't resolve, the chain shows a "Referenced gate not found" banner in-line.
- **Resolved gate** — the drawer mounts in read-only mode (no Approve/Revise/Abort buttons, a "resolved <time-ago>" header).
- **In-app click** — `history.pushState` is used; no full page reload. Browser back pops the query string.
- **New tab / copy link** — the raw URL is a real `<a href>`, so Cmd/Ctrl+click, middle-click, and right-click copy-link all work as expected. A fresh tab bootstraps the resolver.
- **Collapsed chain** — if the target row is under a collapsed section, the section auto-expands before scroll-into-view.

External consumers (future email/Slack relays, IDE plugins) can construct the URL from just `plan_id` + `gate_id` without any Prism-internal knowledge.

---

## Authoring cookbook

Five complete examples covering the common media shapes.

### 1. 2D illustrator (`image-gallery`)

```yaml
review:
  kind: image-gallery
  title: "Sprites & concept art"
  artifact_globs:
    - "Assets/_Project/Art/Generated/**/*.png"
  context_files:
    - ".context/features/*/plan.md"
  panels:
    - type: viewer
    - type: metadata-table
      source: "Assets/_Project/Art/Generated/**/*.meta.yaml"
      item_fields: [width, height, frames, palette]
  coherence_hint: "Match the palette, lighting direction, and line weight of the preserved tiles."
  schema_version: "1"
```

### 2. Animator (`video-preview`)

```yaml
review:
  kind: video-preview
  title: "Cinematics & motion tests"
  artifact_globs:
    - "Assets/_Project/Animation/**/*.mp4"
    - "Assets/_Project/Animation/**/*.webm"
  panels:
    - type: viewer
    - type: list
      source: "Assets/_Project/Animation/**/*.spritesheet.json"
      item_fields: [name, frames, fps]
  partial_revise: true
  schema_version: "1"
```

### 3. Narrative designer (`document`)

```yaml
review:
  kind: document
  title: "Chapter drafts"
  artifact_globs:
    - "Narrative/Chapters/*.md"
  context_files:
    - "Narrative/StyleGuide.md"
  panels:
    - type: markdown
    - type: tree
      source: "Narrative/StoryRoot.yaml"
  notification_template: "{agent_name} delivered {artifact_count} chapter(s)"
  coherence_hint: "Preserve established world-state, character voice, and tense."
  schema_version: "1"
```

### 4. Rigger / 3D modeler (`model-3d-preview`)

```yaml
review:
  kind: model-3d-preview
  title: "Rigged models"
  artifact_globs:
    - "Assets/_Project/Models/**/*.glb"
  panels:
    - type: viewer
    - type: tree
      source: "Assets/_Project/Models/**/*.skeleton.json"
    - type: metadata-table
      item_fields: [tri_count, bone_count, materials]
  schema_version: "1"
```

### 5. Systems / economy designer (`structured-data`)

```yaml
review:
  kind: structured-data
  title: "Balance tables"
  artifact_globs:
    - "Data/Balance/*.yaml"
  panels:
    - type: viewer
    - type: diff
      source: "Data/Balance/*.prev.yaml"
  partial_revise: false
  schema_version: "1"
```

---

## Anti-patterns

Things to **not** declare. Each of these has failed a Prism-based project in review:

- **Persona-shaped core kinds** — `kind: sprite-gallery`, `kind: quest-list`, `kind: rig-view`. Core kinds are media shapes; persona is the agent's domain, not the rendering contract. If you need project-specific rendering, declare an extension kind with `extends: <core>`. The validator catches obvious ones via the `review.kind.persona_shaped` warning; the governance tests (`TestCoreKindIdentity_NoPersonaLeak` in Anthem + `reviewRegistry.test.tsx > governance` in Prism) prevent them from ever entering the core set.
- **Unbounded globs** — `artifact_globs: ["**/*"]` leaks unrelated project files into the review drawer and blows out the file server. Scope to the agent's output directory.
- **Notification spam** — `notification_template` invocations on every micro-step, not just gate opens. The gate_notification event fires once per gate open; don't synthesize extra notifications in agent prompts.
- **Synthesized system-notification for gate opens** — the gate notification is a **real guest-authored chat message**, not a system UI element. Emit it via the normal guest channel so voice, attribution, and preferences work.
- **Panel spam** — stacking every panel type "because we can". Panels should be earning their pixels; the default `viewer` panel on the kind is often enough.
- **`partial_revise: true` on `structured-data`** — structured data is semantically coherent file-level; per-item rerun is almost never what you want. Validator emits `review.partial_revise.redundant`.
- **Baking project names into kind IDs** — `kind: rebeltower-scene-graph`. Ship it as an extension in `.prism/review-extensions.yaml` instead.

---

## Governance

**Stewardship.** Changes to the core-kind registry require a review from a maintainer tagged as a review-kinds steward (role, not person). Their bar: _"could this be expressed by composing existing panels?"_ If yes, reject.

**Deprecation policy.** Removing or renaming a core kind is not done silently:

1. Add the new kind and mark the old kind as deprecated in `kindRegistry` with a `deprecatedAlias` pointing at the replacement.
2. Anthem emits a warning at agent load time when a deprecated kind is used.
3. Old kind is removed no earlier than two minor releases after deprecation.

Prevents "we never delete anything" kind bloat.

**Anti-pattern enforcement.** The governance tests (`TestCoreKindIdentity_NoPersonaLeak` in Anthem, `reviewRegistry.test.tsx > governance` in Prism) are not optional. Adding a persona-shaped kind to get a feature shipped is an architectural tax; it must be paid at the panel layer, not the kind layer.

**PR discipline.** The Anthem and Prism PR templates both include a "Review-kind changes" checklist. Core-kind changes require: doc update here, `kindRegistry` + parity tests, and justification in the PR description for why composition with existing panels is insufficient.

**Cross-repo parity.** `TestCoreKindIdentity_MatchesReviewKindsDoc` (Anthem) parses this document's Core-kinds table and asserts it matches `CoreReviewKinds`. The Prism-side test pins the same list. Either side drifting produces a test failure; the doc is the tiebreaker.
