-- Each job-assistant user's daily search time, spread through the day so
-- no two users' searches run close together.
CREATE TABLE IF NOT EXISTS job_schedules (
    user_id     TEXT PRIMARY KEY REFERENCES users(id),
    at_minute   INTEGER NOT NULL UNIQUE, -- minutes after local midnight
    last_run_on TEXT -- local YYYY-MM-DD of the last search
);
-- Job postings found for a user. A posting's row is made before its
-- application is written, and stays, so it is never found twice.
CREATE TABLE IF NOT EXISTS job_postings (
    user_id      TEXT NOT NULL REFERENCES users(id),
    url_hash     TEXT NOT NULL,
    url          TEXT NOT NULL,
    title        TEXT NOT NULL,
    company      TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL, -- greenhouse, lever, ashby, workday, other
    status       TEXT NOT NULL, -- picked, published, failed, closed, passed (not a fit)
    found_at     INTEGER NOT NULL,
    published_at INTEGER,
    closed_at    INTEGER,
    PRIMARY KEY (user_id, url_hash)
);
-- The web app profile of each user: its Firebase Auth uid,
-- under which their applications and links are kept.
CREATE TABLE IF NOT EXISTS web_profiles (
    user_id TEXT PRIMARY KEY REFERENCES users(id),
    uid     TEXT NOT NULL UNIQUE
);
-- Posting links a user asked for in Slack (the submit_job_links tool),
-- until the turn's harness queues them in the web app.
CREATE TABLE IF NOT EXISTS job_link_requests (
    user_id      TEXT NOT NULL REFERENCES users(id),
    url          TEXT NOT NULL,
    requested_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, url)
);
