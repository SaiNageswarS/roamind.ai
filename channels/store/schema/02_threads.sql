-- Slack thread → claude session. Conversation content lives only in
-- claude's session files.
CREATE TABLE IF NOT EXISTS threads (
    channel_id        TEXT NOT NULL,
    thread_ts         TEXT NOT NULL,
    user_id           TEXT NOT NULL REFERENCES users(id),
    claude_session_id TEXT NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    PRIMARY KEY (channel_id, thread_ts)
);
