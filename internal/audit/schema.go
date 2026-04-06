package audit

const CreateTableSQL = `
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    event_type TEXT NOT NULL,
    task_id TEXT,
    session_id TEXT,
    wave_id TEXT,
    action_name TEXT,
    action_input TEXT,
    action_output TEXT,
    risk_level TEXT,
    auto_approved INTEGER,
    cost_usd REAL,
    error TEXT,
    metadata TEXT
);

CREATE INDEX IF NOT EXISTS idx_events_task_id ON events(task_id);
CREATE INDEX IF NOT EXISTS idx_events_event_type ON events(event_type);
CREATE INDEX IF NOT EXISTS idx_events_wave_id ON events(wave_id);
CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);

CREATE TABLE IF NOT EXISTS traces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp TEXT NOT NULL,
    trace_type TEXT NOT NULL,
    task_id TEXT,
    session_id TEXT,
    wave_id TEXT,
    prompt_hash TEXT,
    prompt_preview TEXT,
    response_preview TEXT,
    tokens_in INTEGER,
    tokens_out INTEGER,
    cost_usd REAL,
    duration_ms INTEGER,
    actions_json TEXT,
    reasoning TEXT,
    review_passed INTEGER,
    review_feedback TEXT,
    metadata TEXT
);

CREATE INDEX IF NOT EXISTS idx_traces_task_id ON traces(task_id);
CREATE INDEX IF NOT EXISTS idx_traces_wave_id ON traces(wave_id);
CREATE INDEX IF NOT EXISTS idx_traces_trace_type ON traces(trace_type);
CREATE INDEX IF NOT EXISTS idx_traces_timestamp ON traces(timestamp);
`
