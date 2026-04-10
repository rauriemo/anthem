---
name: Tooled Agent
description: An agent with tool policy fields
role: specialist
allowed_tools:
  - "mcp__mcp-unity__*"
  - "http__image_gen__*"
  - WebSearch
mcp_servers:
  mcp-unity:
    command: npx
    args: ["-y", "mcp-unity"]
    env:
      UNITY_PORT: "8080"
http_tools:
  image_gen:
    url: https://api.example.com/generate
    method: POST
    auth_token_env: IMAGE_API_KEY
    auth_scheme: bearer
    request_template:
      prompt: "${input.prompt}"
    response_artifact:
      type: image/png
      save_to: "assets/generated/${input.name}.png"
    timeout_ms: 30000
    description: Generate images via API
---

I am a tooled agent with MCP and HTTP tool access.
