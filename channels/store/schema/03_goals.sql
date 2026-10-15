-- One channel per goal, created by the harness; the agent never sees it.
-- Several users can share a channel, each linked to the goal in their own
-- memory.
CREATE TABLE IF NOT EXISTS goal_channels (
    user_id        TEXT NOT NULL REFERENCES users(id),
    goal_permalink TEXT NOT NULL, -- Basic Memory permalink of the goal note
    goal_title     TEXT NOT NULL,
    channel_id     TEXT NOT NULL,
    created_at     INTEGER NOT NULL,
    PRIMARY KEY (user_id, goal_permalink),
    UNIQUE (channel_id, user_id)
);
-- Daily exercises posted for learning goals; a reply in the exercise's
-- thread answers it.
CREATE TABLE IF NOT EXISTS exercises (
    user_id        TEXT NOT NULL REFERENCES users(id),
    goal_permalink TEXT NOT NULL,
    channel_id     TEXT NOT NULL,
    thread_ts      TEXT NOT NULL, -- the exercise message
    note_permalink TEXT NOT NULL, -- the exercise note in the user's memory
    posted_at      INTEGER NOT NULL,
    answered_at    INTEGER, -- first reply in the thread; NULL until then
    PRIMARY KEY (channel_id, thread_ts)
);
CREATE INDEX IF NOT EXISTS exercises_goal ON exercises (user_id, goal_permalink, posted_at);
