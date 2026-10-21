-- Digests posted in news channels, one per user and slot. A digest's row is
-- made before it is posted, to reserve its slot, and completed after.
CREATE TABLE IF NOT EXISTS digests (
    id         INTEGER PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id),
    channel_id TEXT NOT NULL,
    ts         TEXT, -- the digest message; NULL while the slot is reserved
    slot       TEXT NOT NULL, -- local "YYYY-MM-DD HH" it was posted for
    text       TEXT NOT NULL DEFAULT '', -- the items as posted, for its thread
    posted_at  INTEGER NOT NULL, -- when reserved, then when posted
    UNIQUE (user_id, slot),
    UNIQUE (channel_id, ts)
);
CREATE TABLE IF NOT EXISTS digest_items (
    id        INTEGER PRIMARY KEY,
    digest_id INTEGER NOT NULL REFERENCES digests(id),
    interest  TEXT NOT NULL,
    title     TEXT NOT NULL,
    url       TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS digest_items_digest ON digest_items (digest_id);
-- How the user took to a digest: reactions to it and replies in its
-- thread.
CREATE TABLE IF NOT EXISTS digest_signals (
    user_id   TEXT NOT NULL REFERENCES users(id),
    digest_id INTEGER NOT NULL REFERENCES digests(id),
    kind      TEXT NOT NULL, -- reaction, reply
    value     INTEGER NOT NULL, -- +1, 0 or -1
    at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS digest_signals_user ON digest_signals (user_id, at);
