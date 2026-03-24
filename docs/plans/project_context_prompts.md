# Project Context for Orchestrator Agent -- Execution Prompts

These prompts implement the feature that gives the orchestrator agent visibility into the project's file structure and key documentation. Run them in order.

---

## Prompt 1: Add ProjectContext type and wire into StateSnapshot

**File to modify**: `internal/orchestrator/orchagent.go`

Add a `ProjectContext` struct and include it in `StateSnapshot`. This is the data structure that carries codebase awareness to the orchestrator agent.

```
In internal/orchestrator/orchagent.go, add a ProjectContext struct after UserMessageContext:

type ProjectContext struct {
    FileTree       string `json:"file_tree"`
    Architecture   string `json:"architecture,omitempty"`
    Implementation string `json:"implementation,omitempty"`
    ProjectSummary string `json:"project_summary,omitempty"`
}

Then add a Project field to StateSnapshot:

type StateSnapshot struct {
    Tasks        []TaskSummary       `json:"tasks"`
    RetryQueue   []RetrySummary      `json:"retry_queue,omitempty"`
    Budget       BudgetSummary       `json:"budget"`
    Wave         *WaveSummary        `json:"wave,omitempty"`
    RecentEvents []EventSummary      `json:"recent_events,omitempty"`
    UserMessage  *UserMessageContext `json:"user_message,omitempty"`
    Project      *ProjectContext     `json:"project,omitempty"`
}

Do NOT modify snapshotHash -- project context is static between ticks and should not affect the dirty-check.

Run: go build ./cmd/anthem && go vet ./...
```

---

## Prompt 2: Implement file tree generator

**File to modify**: `internal/orchestrator/orchestrator.go`

Add a `generateFileTree` function that walks a directory and produces an indented tree string. This gives the orchestrator agent a map of the codebase.

```
In internal/orchestrator/orchestrator.go, add a function generateFileTree(root string, maxDepth int) (string, error).

Requirements:
- Walk the directory tree using filepath.WalkDir
- Produce indented output like:
    cmd/
      anthem/
        main.go
        templates.go
    internal/
      channel/
        channel.go
        manager.go
        slack/
          adapter.go
- Skip these directories entirely (do not descend into them): .git, vendor, node_modules, workspaces, .idea, .vscode, .claude, .cursor
- Skip files matching: *.exe, *.dll, *.so, *.dylib, *.out, *.swp, *.swo, .DS_Store, Thumbs.db
- Respect maxDepth (count from root=0). If maxDepth is 0 or negative, use a default of 6.
- Cap total output at 8KB. If the tree exceeds this, truncate and append "\n... (truncated)"
- The root directory name itself should NOT appear in the output -- start with its children
- Use 2-space indentation per level
- Sort entries: directories first, then files, both alphabetical

Run: go build ./cmd/anthem && go vet ./...
```

---

## Prompt 3: Implement doc loader with caching

**File to modify**: `internal/orchestrator/orchestrator.go`

Add a method that loads key documentation files and caches them on the Orchestrator struct. These docs give the orchestrator the same context that executor agents get from CLAUDE.md.

```
In internal/orchestrator/orchestrator.go:

1. Add a new field to the Orchestrator struct:
    projectCtx *ProjectContext

2. Add a method loadProjectContext() that:
   - Calls generateFileTree(o.cfg.Workspace.Root, 6) for the file tree. If workspace root is empty or ".", use the current working directory.
   - Reads these files relative to the current working directory (not workspace root, since these are project-level docs):
     * CLAUDE.md -> projectCtx.ProjectSummary
     * docs/plans/architecture.md -> projectCtx.Architecture
     * docs/plans/implementation.md -> projectCtx.Implementation
   - For each file: if it doesn't exist, leave the field empty (no error). If it exists, read it. If the content exceeds 8KB, truncate to 8KB and append "\n[truncated]".
   - Store the result in o.projectCtx

3. Call loadProjectContext() once at the start of the Run() method, right after LoadAndReconcile and before the first tick.

4. Also call loadProjectContext() inside ReloadConfig() so hot-reload picks up doc changes.

Run: go build ./cmd/anthem && go vet ./...
```

---

## Prompt 4: Wire project context into buildStateSnapshot

**File to modify**: `internal/orchestrator/orchestrator.go`

Connect the cached project context to every state snapshot sent to the orchestrator agent.

```
In internal/orchestrator/orchestrator.go, modify buildStateSnapshot to include the cached project context:

At the end of buildStateSnapshot, before "return snap", add:

    snap.Project = o.projectCtx

This is a pointer assignment -- the same cached ProjectContext is shared across all snapshots. This is safe because ProjectContext is only written during loadProjectContext() which runs before ticks start and during hot-reload (which is serialized).

Run: go build ./cmd/anthem && go vet ./...
```

---

## Prompt 5: Update orchestrator system prompt

**File to modify**: `internal/orchestrator/orchagent.go`

Add instructions to the system prompt telling the orchestrator how to use the project context.

```
In internal/orchestrator/orchagent.go, in the buildSystemPrompt function, add a new section AFTER the "## Multi-Format Input" section and BEFORE the "## Wave Model" section:

sections = append(sections, `## Project Context

The state snapshot includes a "project" field containing:
- file_tree: the project's directory structure showing all source files
- project_summary: contents of CLAUDE.md with design decisions and current status
- architecture: contents of docs/plans/architecture.md with system design
- implementation: contents of docs/plans/implementation.md with build plan and phase status

Use this context to:
- Understand the codebase structure when decomposing features into subtasks
- Reference specific files and modules when writing subtask descriptions
- Respect architectural decisions documented in the project summary
- Understand what has been built (completed phases) vs what is planned (future phases)
- Write subtask bodies that reference the correct file paths and existing patterns`)

Run: go build ./cmd/anthem && go vet ./...
```

---

## Prompt 6: Add tests

**Files to create/modify**: `internal/orchestrator/context_test.go`

```
Create a new file internal/orchestrator/context_test.go in package orchestrator with these tests:

1. TestGenerateFileTree_BasicStructure:
   - Create a temp directory with this structure:
     src/main.go, src/util.go, pkg/lib/helper.go, README.md
   - Call generateFileTree(tempDir, 6)
   - Assert output contains "src/", "main.go", "pkg/", "lib/", "helper.go", "README.md"
   - Assert directories appear before files at same level

2. TestGenerateFileTree_SkipsExcludedDirs:
   - Create a temp directory with: src/main.go, .git/config, node_modules/pkg/index.js, vendor/lib.go
   - Call generateFileTree(tempDir, 6)
   - Assert output contains "src/" and "main.go"
   - Assert output does NOT contain ".git", "node_modules", "vendor"

3. TestGenerateFileTree_RespectsMaxDepth:
   - Create a deeply nested structure: a/b/c/d/e/f/deep.go
   - Call generateFileTree(tempDir, 3)
   - Assert output contains "a/", "b/", "c/" but NOT "d/" or "deep.go"

4. TestGenerateFileTree_Truncation:
   - Create a temp dir with many files (e.g. 500 files) so the tree exceeds 8KB
   - Call generateFileTree(tempDir, 6)
   - Assert output length <= 8KB + len("... (truncated)")
   - Assert output ends with "... (truncated)"

5. TestBuildStateSnapshot_IncludesProjectContext:
   - Create an Orchestrator with a non-nil projectCtx containing a test file tree and project summary
   - Call buildStateSnapshot with empty task list
   - Assert snap.Project is not nil
   - Assert snap.Project.FileTree matches the set value

6. TestBuildSystemPrompt_ContainsProjectContextSection:
   - Call buildSystemPrompt("")
   - Assert the result contains "## Project Context"
   - Assert the result contains "file_tree"

Use the standard testing package, table-driven tests where appropriate, t.TempDir() for temp directories, and os.MkdirAll + os.WriteFile for creating test fixtures.

Run: go test ./internal/orchestrator/ -run TestGenerate -v && go test ./internal/orchestrator/ -run TestBuildState -v && go test ./internal/orchestrator/ -run TestBuildSystem -v
```

---

## Prompt 7: Run full verification

```
Run these commands in sequence from the project root:

go build ./cmd/anthem
go vet ./...
go test ./...
golangci-lint run ./...

Fix any errors. All must pass.
```

---

## Prompt 8: Update documentation

**Files to modify**: `CLAUDE.md`, `docs/plans/architecture.md`, `docs/plans/implementation.md`

```
Update documentation to reflect the new project context feature:

1. In CLAUDE.md, in the "Phase 3b completed" section, add a bullet:
   - Project context enrichment: orchestrator agent now receives the project file tree, CLAUDE.md, architecture.md, and implementation.md in every StateSnapshot via the ProjectContext struct. Loaded once at startup and on config hot-reload. File tree generated by walking workspace root with depth limit and exclusion patterns. Doc contents truncated at 8KB. This gives the orchestrator the same codebase awareness as executor agents for informed task decomposition.

2. In docs/plans/architecture.md, find the section describing the orchestrator agent's state snapshot or data flow. Add a subsection or paragraph explaining:
   - StateSnapshot now includes a Project field (ProjectContext) with file_tree, architecture, implementation, and project_summary
   - Loaded at startup via loadProjectContext(), refreshed on hot-reload
   - File tree generated by generateFileTree() with configurable depth, directory exclusions (.git, vendor, node_modules, workspaces, .idea, .vscode, .claude, .cursor), and 8KB cap
   - Doc files read from project root with 8KB truncation
   - Static between ticks -- excluded from snapshotHash dirty-check

3. In docs/plans/implementation.md, if there is a Phase 3b section, add the project context feature as a completed item. If there is a checklist or status tracker, mark it done.

Do NOT change any code files. Only update .md documentation files.
```
