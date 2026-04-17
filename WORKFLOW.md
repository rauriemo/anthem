---
tracker:
  kind: github
  repo: "rauriemo/anthem"
  labels:
    active: ["todo"]
    terminal: ["done"]

polling:
  interval_ms: 10000

workspace:
  root: "./workspaces"

hooks:
  after_create: "git clone {{issue.repo_url}} ."
  before_run: "git pull origin main"

agent:
  command: "claude"
  max_turns: 5
  max_concurrent: 3
  stall_timeout_ms: 300000
  max_retry_backoff_ms: 300000
  mcp_servers:
    # Scheduling & cron infrastructure (send_email, create_calendar_event,
    # schedule_email, schedule_recurring, list_scheduled, cancel)
    scheduler:
      type: stdio
      command: "C:\\Users\\I9 Ultra\\alfred\\bin\\alfred.exe"
      args:
        - scheduler
        - stdio
    # Gmail (read inbox, search, draft)
    gmail:
      type: stdio
      command: npx
      args:
        - "-y"
        - "@gongrzhe/server-gmail-autoauth-mcp"
      env:
        GMAIL_OAUTH_CREDENTIALS: "C:\\Users\\I9 Ultra\\.alfred\\google-credentials.json"
        GMAIL_OAUTH_TOKEN: "C:\\Users\\I9 Ultra\\.alfred\\google-token.json"
    # Google Calendar (view events, check availability)
    google-calendar:
      type: stdio
      command: npx
      args:
        - "-y"
        - "@nspady/google-calendar-mcp"
      env:
        GOOGLE_OAUTH_CREDENTIALS: "C:\\Users\\I9 Ultra\\.alfred\\google-credentials.json"
        GOOGLE_OAUTH_TOKEN: "C:\\Users\\I9 Ultra\\.alfred\\google-token.json"

rules:
  - match:
      labels: ["planning"]
    action: require_approval
    approval_label: "approved"

system:
  constraints:
    - "Run all tests before merging"
    - "Do not commit secrets or credentials"

channels:
  - kind: slack
    target: "C0ANBSDP40N"
    events:
      - task.completed
      - task.failed
      - task.waiting_approval
      - task.budget_exceeded
      - maintenance.suggested
      - wave.completed
  - kind: dispatch
    target: "localhost:8081"
    events:
      - task.completed
      - task.failed
      - maintenance.suggested
  - kind: prism
    target: "localhost:3105"
    events:
      - task.completed
      - task.failed
      - maintenance.suggested
  - kind: voice
    target: "anthem-voice"
server:
  port: 8080
---

You are an expert software engineer working on {{.issue.title}}.

Repository: {{.issue.repo_url}}
Branch: anthem/{{.issue.identifier}}

## Task
{{.issue.body}}

## Rules
- Create a branch named `anthem/{{.issue.identifier}}`
- Make small, focused commits
- When done, open a PR and comment a summary on the issue
