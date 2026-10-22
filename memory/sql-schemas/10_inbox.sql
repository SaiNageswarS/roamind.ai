-- The digests posted in users' inbox channels, one a day at most: what
-- needs their attention, from their items that are due.
CREATE TABLE IF NOT EXISTS inbox_digests (
    id         INTEGER PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    day        TEXT NOT NULL, -- local ISO date it was posted for
    channel_id TEXT NOT NULL,
    ts         TEXT, -- the digest message; NULL while the day is reserved
    text       TEXT NOT NULL DEFAULT '', -- the digest as posted
    posted_at  INTEGER NOT NULL, -- when reserved, then when posted
    UNIQUE (user_id, day)
);
