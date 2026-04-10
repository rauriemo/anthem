---
name: Bad Auth Agent
description: An agent with invalid auth_scheme
http_tools:
  bad_tool:
    url: https://api.example.com/bad
    method: GET
    auth_scheme: basic
---

Invalid auth scheme agent.
