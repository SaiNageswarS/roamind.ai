-- People known to the agent, keyed by email hash, with their Slack identity
-- and Basic Memory project.
CREATE TABLE IF NOT EXISTS users (
    id               TEXT PRIMARY KEY, -- sha256(lowercase(trim(email)))
    email            TEXT NOT NULL UNIQUE,
    display_name     TEXT NOT NULL DEFAULT '',
    slack_team_id    TEXT,
    slack_user_id    TEXT,
    memory_project   TEXT UNIQUE, -- Basic Memory project, bm-u-<name>-<idx>
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    UNIQUE (slack_team_id, slack_user_id)
);
