# Draft Email

## When to Use
Activate when Rafael asks to write, reply to, or forward an email.

## MCP Tools Required
- **gmail**: `get_email`, `search_emails` (read-only — for fetching context and threads)
- **scheduler**: `send_email` (immediate send), `schedule_email` (deferred send)

## Voice Guide

Write as Rafael, not as Alfred. Alfred is invisible to the outside world.

- **Professional contacts:** Warm but direct. No corporate filler. Get to the point in the first sentence. Close with something human, not "Best regards" unless the relationship demands it.
- **Friends / casual:** Conversational. Contractions are fine. Match the energy of the thread.
- **Cold outreach:** Respectful of their time. Lead with why they should care. Short.
- **Follow-ups:** Reference the prior exchange specifically. Don't re-explain what they already know.

## Procedure

1. **Gather context.** If replying, fetch the thread with gmail `get_email` to understand the conversation. Note tone, names, prior commitments.
2. **Draft.** Write the email body. Match formality to the recipient. Include subject line if new thread.
3. **Send or schedule — choose the right path:**
   - **Rafael waived approval** ("just send it", "no approval needed"): use the Scheduler's `send_email` tool immediately in this same turn. Do NOT ask for confirmation.
   - **Deferred send:** use `schedule_email` with the specified future time.
   - **Approval needed (default):** schedule the email to send in 3 minutes via `schedule_email`. Present the full draft (To, CC, Subject, Body) and the job ID. Tell Rafael: *"Scheduled to send in 3 minutes. Reply 'cancel [job_id]' to stop."* This ensures the content is persisted even if context is lost between turns.
4. **Revise if needed.** If Rafael asks for changes, `cancel` the pending job, revise, and re-schedule.

NEVER use the Gmail MCP to send — it only supports `create_draft`.

## Rules
- Sign as Rafael unless told otherwise.
- No emoji in professional emails unless Rafael's style with that contact includes them.
- If the email involves a commitment (meeting, deadline, deliverable), flag it explicitly before sending.
- For replies, always quote relevant context. Don't top-post into a void.
