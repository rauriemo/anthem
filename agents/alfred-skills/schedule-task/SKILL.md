# Schedule Task

## When to Use
Activate when Rafael wants to defer an action to a specific time or set up a recurring job.

## MCP Tools Required
- **scheduler**: `schedule_email`, `schedule_reminder`, `schedule_calendar_event`, `schedule_recurring`, `list_scheduled`, `cancel`

## Scheduling Tools

### One-Shot Actions
- `schedule_email(to, cc, subject, body, at)` — send an email at a future time.
- `schedule_reminder(message, at, channel?)` — fire a reminder. v1 channels: `log`, `webhook`.
- `schedule_calendar_event(summary, start, end, ...)` — create a calendar event at a future time (or immediately if `at` is omitted/now).

### Recurring Actions
- `schedule_recurring(kind, cron_expr, payload)` — create a cron-driven job.
  - `kind`: `email`, `reminder`, `inbox_digest`
  - `cron_expr`: standard 5-field cron (minute hour day-of-month month day-of-week)

### Management
- `list_scheduled(filter?)` — view pending, done, failed, or cancelled jobs.
- `cancel(job_id)` — cancel a pending or recurring job.

## Cron Expression Reference
```
┌───────────── minute (0-59)
│ ┌───────────── hour (0-23)
│ │ ┌───────────── day of month (1-31)
│ │ │ ┌───────────── month (1-12)
│ │ │ │ ┌───────────── day of week (0-6, Sun=0)
│ │ │ │ │
* * * * *
```
Examples:
- `0 9 * * 1-5` — weekdays at 9:00 AM
- `30 17 * * *` — daily at 5:30 PM
- `0 8 1 * *` — first of every month at 8:00 AM

## Procedure

1. **Clarify the intent.** What action? When? One-time or recurring?
2. **Translate to tool call.** Map Rafael's request to the correct scheduler tool. Parse natural language times into ISO 8601 or cron expressions.
3. **Present for approval.** Show the exact tool call: action, timing, payload. For cron, explain in plain English what the schedule means.
4. **Execute.** Call the scheduler tool. Return the `job_id` to Rafael.
5. **Confirm.** Report success or failure. For recurring jobs, mention when the next fire will be.

## Rules
- **Never schedule without approval.** Always present the action and timing first.
- Prefer explicit times over relative ("at 5 PM" not "in 3 hours") to avoid ambiguity.
- For recurring jobs, always explain the cron expression in plain English.
- If a scheduled email will go out while Rafael might be asleep, flag it — he may want a different send time.
- When listing scheduled jobs, group by status and sort by fire time.
