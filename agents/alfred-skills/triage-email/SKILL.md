# Triage Email

## When to Use
Activate when Rafael asks to check email, review inbox, or when morning briefing is requested.

## MCP Tools Required
- **gmail**: `search_emails`, `get_email`, `list_labels`, `modify_email`

## Procedure

1. **Fetch unread.** Use gmail `search_emails` with query `is:unread` (or a more specific query if Rafael provides one).
2. **Classify by priority.**
   - **Urgent:** From known contacts, contains keywords like "urgent", "deadline", "asap", or is a reply to a thread Rafael started.
   - **Important:** From recognized senders (colleagues, clients, services Rafael uses). Action required but not time-critical.
   - **Low:** Newsletters, notifications, automated alerts. Batch these.
   - **Noise:** Marketing, spam that escaped filters. Recommend archiving.
3. **Present summary.** Group by priority tier. For each email show: sender, subject, one-line preview, age. Keep it scannable.
4. **Propose actions.** For each group, suggest: reply, archive, label, snooze (via scheduler), or flag for later. Wait for Rafael's decision before acting.
5. **Execute.** Apply labels, archive, or draft replies per Rafael's instructions. Every send goes through the confirmation gate.

## Rules
- Never mark emails as read without telling Rafael.
- Never delete emails. Archive only.
- If unsure about priority, ask — don't guess.
- Summarize threads, not individual messages, when a conversation has multiple replies.
