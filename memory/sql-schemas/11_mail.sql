-- Gmail accounts whose Apps Script sends their mail to the gmail webhook,
-- each belonging to one user (task mail-connect). A user may have many.
CREATE TABLE IF NOT EXISTS mail_accounts (
    id           TEXT PRIMARY KEY, -- lowercase address
    address      CITEXT NOT NULL UNIQUE,
    user_id      TEXT NOT NULL REFERENCES users(id),
    -- The account's own key, hex: its script signs each request with it,
    -- so one account's script can't send mail as another's. The webhook's
    -- secret, which every script has, only keeps others out.
    secret       TEXT NOT NULL,
    connected_at BIGINT NOT NULL,
    seen_at      BIGINT, -- the script's last request, heartbeats included; NULL before its first
    created_on   BIGINT NOT NULL DEFAULT 0, -- unix seconds, set by go-api-boot's Save
    updated_on   BIGINT NOT NULL DEFAULT 0
);
-- Messages the scripts sent. A message's row is made before its turn and
-- stays, so a message sent again is not related twice.
CREATE TABLE IF NOT EXISTS mail_messages (
    address     CITEXT NOT NULL,
    message_id  TEXT NOT NULL, -- Gmail's message id
    user_id     TEXT NOT NULL REFERENCES users(id),
    category    TEXT NOT NULL, -- the script's: bill, travel, order, appointment, commitment, person, other; finance for mail read into the ledger
    subject     TEXT NOT NULL,
    link        TEXT NOT NULL, -- the message in Gmail
    status      TEXT NOT NULL, -- picked, done, skipped (nothing to save), failed
    picked_at   BIGINT NOT NULL, -- when its latest run started
    done_at     BIGINT,
    PRIMARY KEY (address, message_id)
);
