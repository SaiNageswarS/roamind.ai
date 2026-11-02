-- Gmail accounts whose Apps Script sends their mail to the gmail webhook,
-- each belonging to one user (task mail-connect). A user may have many.
CREATE TABLE IF NOT EXISTS mail_accounts (
    address      TEXT PRIMARY KEY COLLATE NOCASE,
    user_id      TEXT NOT NULL REFERENCES users(id),
    connected_at INTEGER NOT NULL,
    seen_at      INTEGER -- the script's last request, heartbeats included; NULL before its first
);
-- Messages the scripts sent. A message's row is made before its turn and
-- stays, so a message sent again is not related twice.
CREATE TABLE IF NOT EXISTS mail_messages (
    address     TEXT NOT NULL COLLATE NOCASE,
    message_id  TEXT NOT NULL, -- Gmail's message id
    user_id     TEXT NOT NULL REFERENCES users(id),
    category    TEXT NOT NULL, -- the script's: bill, travel, order, appointment, commitment, person, other
    status      TEXT NOT NULL, -- picked, done, skipped (nothing to save), failed
    received_at INTEGER NOT NULL,
    done_at     INTEGER,
    PRIMARY KEY (address, message_id)
);
