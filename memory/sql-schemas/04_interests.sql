-- The catalog of news interests: each with its curated feeds and the
-- rubric its items are ranked by. The curated ones (owner '') come from
-- memory/seed/interests.sql and are seen by everyone; the agent
-- adds a user's own, with feeds it has checked, seen only by that user.
CREATE TABLE IF NOT EXISTS interests (
    id         INTEGER PRIMARY KEY,
    interest   TEXT NOT NULL COLLATE NOCASE,
    owner      TEXT NOT NULL DEFAULT '', -- users.id of the user who added it; '' when curated
    sources    TEXT NOT NULL, -- JSON array of RSS/Atom feed URLs
    rubric     TEXT NOT NULL, -- prose: the scores (0-10), when to include, categories
    is_default INTEGER NOT NULL DEFAULT 0, -- 1 for curated interests every user follows
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (owner, interest)
);
-- The interests each user follows.
CREATE TABLE IF NOT EXISTS user_interests (
    user_id     TEXT NOT NULL REFERENCES users(id),
    interest_id INTEGER NOT NULL REFERENCES interests(id) ON DELETE CASCADE,
    status      TEXT NOT NULL DEFAULT 'active', -- active, muted
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, interest_id)
);
-- The links sent to each user in digests, so none is sent twice.
CREATE TABLE IF NOT EXISTS sent_urls (
    user_id  TEXT NOT NULL REFERENCES users(id),
    url_hash TEXT NOT NULL, -- URLHash of the link
    sent_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, url_hash)
);
-- The stories sent to each user, as the ranking's story slugs, so several
-- sources reporting one event make one item.
CREATE TABLE IF NOT EXISTS sent_stories (
    user_id TEXT NOT NULL REFERENCES users(id),
    story   TEXT NOT NULL, -- lowercase-hyphenated slug naming the event
    title   TEXT NOT NULL DEFAULT '', -- the title of the item sent for it
    sent_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, story)
);
