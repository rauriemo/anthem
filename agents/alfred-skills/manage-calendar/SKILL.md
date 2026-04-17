# Manage Calendar

## When to Use
Activate when Rafael asks about schedule, availability, meetings, or anything calendar-related.

## MCP Tools Required
- **google-calendar**: `list_events`, `get_event` (read-only — for viewing schedule and availability)
- **scheduler**: `create_calendar_event` (immediate), `schedule_calendar_event` (deferred), `schedule_reminder`

**Note:** The Google Calendar MCP is read-only. To CREATE, UPDATE, or DELETE events, use the Scheduler's `create_calendar_event` tool.

## Procedure

### Viewing Schedule
1. Fetch events for the requested window (default: today and tomorrow).
2. Present as a clean timeline: time, title, location, attendees. Flag conflicts.
3. If asked about availability, use `get_freebusy` to identify open slots.

### Creating Events
1. Confirm: summary, start/end times, attendees, location, description.
2. Check for conflicts against existing events. Warn if overlap found.
3. Present the event details and ask for approval before creating.
4. Create via the Scheduler's `create_calendar_event` tool. If the event is in the future and Rafael wants a reminder, also use `schedule_reminder`.

### Updating Events
1. Fetch the existing event. Show current vs proposed changes.
2. Confirm the changes with Rafael.
3. Execute via `update_event`.

### Deleting Events
1. Show the event to be deleted. Confirm with Rafael.
2. If the event has other attendees, suggest sending a cancellation. Confirm.
3. Execute via `delete_event`.

## Preferences
- Default meeting length: 30 minutes unless specified.
- Buffer: prefer 15-minute gaps between meetings.
- Working hours: respect whatever Rafael defines. Don't schedule outside them unless he explicitly asks.
- Time zones: always confirm if the other party is in a different zone.

## Rules
- **Never create, update, or delete events without approval.**
- If a proposed meeting time conflicts, present alternatives before asking to override.
- For recurring meetings, confirm the recurrence pattern explicitly.
