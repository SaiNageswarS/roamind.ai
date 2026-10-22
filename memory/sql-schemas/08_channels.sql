-- The Slack channels the harness made for users, one per purpose: one per
-- goal, the user's news shorts and their inbox. The agent never sees them. Several users
-- can share a channel, each mapped to it by their own row, for their own
-- goal in their own memory.
CREATE TABLE IF NOT EXISTS channels (
    user_id    TEXT NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL, -- goal, news, inbox
    ref        TEXT NOT NULL DEFAULT '', -- the goal note's Basic Memory permalink for goal; '' otherwise
    title      TEXT NOT NULL DEFAULT '', -- the goal's title for goal; '' otherwise
    channel_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, kind, ref),
    UNIQUE (channel_id, user_id)
);
