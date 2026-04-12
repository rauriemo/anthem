---
name: Async Agent
description: Agent with async polling HTTP tool
role: animator
capabilities:
  - video generation
icon: clapperboard
http_tools:
  veo:
    url: "https://generativelanguage.googleapis.com/v1beta/models/veo-3.1-generate-preview:predictLongRunning"
    method: POST
    auth_token_env: GEMINI_API_KEY
    auth_scheme: api-key
    async_polling:
      enabled: true
      poll_interval_ms: 10000
      max_wait_ms: 300000
      operation_name_path: "name"
      done_path: "done"
      result_path: "response.generateVideoResponse.generatedSamples[0].video.uri"
      download_auth: inherit
    request_template:
      instances:
        - prompt: "${input.prompt}"
    response_artifact:
      type: video/mp4
      save_to: "output/${input.filename}"
    timeout_ms: 300000
    description: "Generate video via Veo 3.1"
allowed_tools:
  - "http__veo__*"
---

You are a test agent with async polling capabilities.
