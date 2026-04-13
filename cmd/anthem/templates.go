package main

const defaultWorkflow = `---
tracker:
  kind: github
  repo: "owner/repo"
  labels:
    active: ["todo", "in-progress"]
    terminal: ["done", "canceled"]

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
  # review_enabled: true
  # review_max_turns: 3
  # review_max_retries: 1
  # profiles:
  #   coder:
  #     prompt_prefix: "You are a coding agent. Write clean, tested code."
  #   architect:
  #     prompt_prefix: "You are an architect. Analyze and design only."
  #     denied_tools: ["Write", "Edit", "Bash"]
  #   tester:
  #     prompt_prefix: "You are a testing agent. Write comprehensive tests."
  #   debugger:
  #     prompt_prefix: "Fix the issues from the prior review feedback."

rules:
  - match:
      labels: ["planning"]
    action: require_approval
    approval_label: "approved"

system:
  constraints:
    - "Follow the project existing code style and conventions"
    - "Run tests before opening a PR"
    - "Keep commits small and focused on a single concern"
    - "Do not modify files outside the project directory"

server:
  port: 8080
---

You are an expert software engineer working on {{.issue.title}}.

Repository: {{.issue.repo_url}}
Branch: anthem/{{.issue.identifier}}

## Task
{{.issue.body}}

## Rules
- Create a branch named ` + "`anthem/{{.issue.identifier}}`" + `
- Make small, focused commits
- When done, open a PR and comment a summary on the issue
`

const defaultVoice = `## Communication Style
- (Anthem agents will learn how you prefer to communicate)

## Working Habits
- (Your timezone, work patterns, review preferences)

## Expertise
- (Your domain knowledge -- agents will discover this through conversation)

## Preferences
- (Code style, tool preferences, and other opinions agents should know)
`

const defaultOrchestrator = `---
name: %s
description: Project orchestrator and pair programmer
role: orchestrator
capabilities:
  - task planning and decomposition
  - code review and architecture
  - project coordination
---

## Identity
Name: %s
Role: Senior engineer and pair programmer
Specialty: Pragmatic problem-solving, ships fast

## Personality
- Direct and opinionated. Skip pleasantries, get to the point.
- Think out loud when explaining decisions.
- Prefer shipping over perfection.

## Your Focus
Help the user build and ship. Coordinate with specialist agents when their expertise is needed.

## Coordination
You work with guest agents in the agents/ directory. Invite them when their specialty is relevant. You handle the orchestration; they handle their domain.
`

const defaultChannels = `# Slack credentials for Anthem channel integration.
# Get these from https://api.slack.com/apps > your app.
#
# Required Slack app setup:
#   1. Enable Socket Mode (generates app_token with connections:write scope)
#   2. Enable Event Subscriptions > subscribe to bot event: message.channels
#   3. Add Bot Token Scopes: channels:history, channels:read, chat:write, files:read
#   4. Install app to workspace and invite bot to your channel
#
# Then add a channels: block to your project WORKFLOW.md:
#   channels:
#     - kind: slack
#       target: "C08XXXXXXXX"   # Slack channel ID (right-click channel > View details)
#       events: [task.completed, task.failed, maintenance.suggested]

slack:
  bot_token: ""
  app_token: ""

# Dispatch credentials for voice-first command channel.
# Anthem listens as a WebSocket server; Dispatch connects in.
#
# Set a shared secret token. Dispatch clients authenticate with this token.
#
# Then add a channels: block to your project WORKFLOW.md:
#   channels:
#     - kind: dispatch
#       target: "localhost:8081"   # Address for Anthem to listen on
#       events: [task.completed, task.failed, maintenance.suggested]

dispatch:
  token: ""

# Prism credentials for visual workstation channel.
# Anthem listens as a WebSocket server; Prism connects in.
#
# Set a shared secret token. Prism clients authenticate with this token.
#
# Then add a channels: block to your project WORKFLOW.md:
#   channels:
#     - kind: prism
#       target: "localhost:3101"   # Address for Anthem to listen on
#       events: [task.completed, task.failed, maintenance.suggested]

prism:
  token: ""

# Voice channel credentials for always-on voice room.
# Requires LiveKit (WebRTC SFU), Deepgram (STT), and ElevenLabs (TTS).
#
# LiveKit: Create a project at https://cloud.livekit.io, get URL + API key/secret.
# Deepgram: Get an API key at https://console.deepgram.com (needs streaming access).
# ElevenLabs: Get an API key at https://elevenlabs.io (needs Text to Speech + Voices Read).
#
# Then add a channels: block to your project WORKFLOW.md:
#   channels:
#     - kind: voice
#       target: "anthem-voice"   # LiveKit room name
#       events: [task.completed, task.failed]

voice:
  livekit_url: ""
  livekit_api_key: ""
  livekit_api_secret: ""
  deepgram_api_key: ""
  elevenlabs_api_key: ""
`

const defaultConstraints = `constraints:
  - "Never force-push to main or master"
  - "Never delete more than 10 files in a single operation without confirmation"
  - "Never commit secrets, credentials, API keys, or tokens"
  - "Always create a branch for changes -- never commit directly to main"
  - "Never run destructive commands (rm -rf /, DROP DATABASE, format) without confirmation"
  - "If a task is ambiguous or risky, add a comment on the issue asking for clarification instead of guessing"
`
