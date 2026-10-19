-- Job searches run at fixed hours for every user; the per-user search
-- times are gone.
DROP TABLE IF EXISTS job_schedules;
-- Job postings found for a user. A posting's row is made before its
-- application is written, and stays, so it is never found twice.
CREATE TABLE IF NOT EXISTS job_postings (
    user_id      TEXT NOT NULL REFERENCES users(id),
    url_hash     TEXT NOT NULL,
    url          TEXT NOT NULL,
    title        TEXT NOT NULL,
    company      TEXT NOT NULL DEFAULT '',
    source       TEXT NOT NULL, -- jobs.Ref.Source: greenhouse, lever, …, other
    status       TEXT NOT NULL, -- picked, published, failed, closed, passed (not a fit)
    found_at     INTEGER NOT NULL,
    published_at INTEGER,
    closed_at    INTEGER,
    PRIMARY KEY (user_id, url_hash)
);
-- Posting links a user asked for in Slack (the submit_job_links tool),
-- until the chat turn's harness starts drafting them.
CREATE TABLE IF NOT EXISTS job_link_requests (
    user_id      TEXT NOT NULL REFERENCES users(id),
    url          TEXT NOT NULL,
    requested_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, url)
);
