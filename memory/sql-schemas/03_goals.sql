-- Daily exercises posted for learning goals; a reply in the exercise's
-- thread answers it.
CREATE TABLE IF NOT EXISTS exercises (
    id             TEXT PRIMARY KEY, -- channel_id|thread_ts
    user_id        TEXT NOT NULL REFERENCES users(id),
    goal_permalink TEXT NOT NULL,
    channel_id     TEXT NOT NULL,
    thread_ts      TEXT NOT NULL, -- the exercise message
    note_permalink TEXT NOT NULL, -- the exercise note in the user's memory
    posted_at      BIGINT NOT NULL,
    answered_at    BIGINT, -- first reply in the thread; NULL until then
    created_on     BIGINT NOT NULL DEFAULT 0, -- unix seconds, set by go-api-boot's Save
    updated_on     BIGINT NOT NULL DEFAULT 0,
    UNIQUE (channel_id, thread_ts)
);
CREATE INDEX IF NOT EXISTS exercises_goal ON exercises (user_id, goal_permalink, posted_at);
