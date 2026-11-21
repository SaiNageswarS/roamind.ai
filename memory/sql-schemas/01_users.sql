-- People known to the agent, keyed by email hash, with the channel they came
-- from, their Slack identity and Basic Memory project.
-- Case-insensitive text, for the columns SQLite compared with COLLATE NOCASE.
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE IF NOT EXISTS users (
    id               TEXT PRIMARY KEY, -- sha256(lowercase(trim(email)))
    email            TEXT NOT NULL UNIQUE,
    display_name     TEXT NOT NULL DEFAULT '',
    channel          TEXT NOT NULL DEFAULT 'slack', -- slack, or web for users who signed up in the web app
    slack_team_id    TEXT,
    slack_user_id    TEXT,
    memory_project   TEXT UNIQUE, -- Basic Memory project, bm-u-<name>-<idx>
    created_on       BIGINT NOT NULL,
    updated_on       BIGINT NOT NULL,
    UNIQUE (slack_team_id, slack_user_id)
);
