---
tracker:
  kind: github
  repo: "test/repo"
  labels:
    active: ["todo"]
    terminal: ["done"]

polling:
  interval_ms: 5000

workspace:
  root: "./test-workspaces"

agent:
  command: "claude"
  max_turns: 3
  max_concurrent: 2

rules:
  - match:
      labels: ["needs-review"]
    action: require_approval
    approval_label: "approved"

system:
  workflow_changes_require_approval: true
  voice_changes_require_approval: false
  voice_core_immutable: true

server:
  port: 9090
---

You are working on {{issue.title}}.

## Task
{{issue.body}}
