-- Inbound Slack events already processed, so a Slack retry runs once.
CREATE TABLE IF NOT EXISTS inbound_events (
    event_id    TEXT PRIMARY KEY, -- Slack event_id
    received_at INTEGER NOT NULL
);
