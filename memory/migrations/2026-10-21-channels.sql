-- One-time: moves goal_channels and news_channels into channels, then drops
-- them. Run once, with the service stopped, before the worker that reads
-- channels starts:
--   sqlite3 -bail data/roamind.db < memory/migrations/2026-10-21-channels.sql
-- It runs in one transaction, so it either moves every row or changes
-- nothing; a second run fails, finding no goal_channels, and changes
-- nothing.
BEGIN;
CREATE TABLE IF NOT EXISTS channels (
    user_id    TEXT NOT NULL REFERENCES users(id),
    kind       TEXT NOT NULL,
    ref        TEXT NOT NULL DEFAULT '',
    title      TEXT NOT NULL DEFAULT '',
    channel_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, kind, ref),
    UNIQUE (channel_id, user_id)
);
INSERT INTO channels (user_id, kind, ref, title, channel_id, created_at)
    SELECT user_id, 'goal', goal_permalink, goal_title, channel_id, created_at FROM goal_channels;
INSERT INTO channels (user_id, kind, ref, title, channel_id, created_at)
    SELECT user_id, 'news', '', '', channel_id, created_at FROM news_channels;
DROP TABLE goal_channels;
DROP TABLE news_channels;
COMMIT;
