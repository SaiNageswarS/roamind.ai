-- The user's tasks and events and their state: whether each is open, done
-- or cancelled, and from when to when it shows as due. The details stay in
-- the Basic Memory note each row comes from; the note has no status.
CREATE TABLE IF NOT EXISTS items (
    user_id    TEXT NOT NULL REFERENCES users(id),
    key        TEXT NOT NULL COLLATE NOCASE, -- a stable title, or the confirmation, order or invoice number
    kind       TEXT NOT NULL, -- the note type: task, event
    event_kind TEXT NOT NULL DEFAULT '', -- the Event's kind (delivery, travel, …), which sets its lead time; '' for tasks
    note       TEXT NOT NULL, -- the permalink of its Basic Memory note
    title      TEXT NOT NULL, -- one line, for the digest
    date       TEXT, -- the task's due or the event's date, ISO date or date-time; NULL if undated
    show_from  TEXT, -- ISO date it first shows: remind_on, or date minus the lead time; NULL never shows
    until      TEXT, -- ISO date it last shows; NULL until it is closed
    state      TEXT NOT NULL, -- open, done, cancelled
    source     TEXT NOT NULL, -- 'chat', or 'mail:<address>/<message id>'
    updated_at INTEGER NOT NULL,
    closed_at  INTEGER,
    PRIMARY KEY (user_id, key)
);
CREATE INDEX IF NOT EXISTS items_due ON items (user_id, state, show_from);
