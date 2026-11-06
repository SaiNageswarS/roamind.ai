-- The finance assistant's state, per user. Its ledger (trade events, trades,
-- prices) is in QuestDB: memory/questdb-schemas.

-- Brokerage accounts, each made on its first report or trade. A report is
-- matched to its account by the broker and the account number it shows.
CREATE TABLE IF NOT EXISTS fin_accounts (
    user_id    TEXT NOT NULL REFERENCES users(id),
    id         TEXT NOT NULL, -- a1, a2, …: the account's key in QuestDB and to the agent
    broker     TEXT NOT NULL, -- lowercase, as the user or the report names it
    number     TEXT NOT NULL, -- the account number as the broker shows it, often masked; '' when none
    currency   TEXT NOT NULL DEFAULT '', -- the account's own currency, when known
    name       TEXT NOT NULL DEFAULT '', -- what the user calls it
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, id),
    UNIQUE (user_id, broker, number)
);

-- Reports imported from uploads, by their SHA-256, so a file uploaded again
-- is refused. Its period is the days its trades cover.
CREATE TABLE IF NOT EXISTS finance_uploads (
    user_id     TEXT NOT NULL REFERENCES users(id),
    sha256      TEXT NOT NULL,
    file        TEXT NOT NULL, -- its name in the user's workspace
    account     TEXT NOT NULL, -- fin_accounts.id
    doc         TEXT NOT NULL, -- tradebook, statement, contract_note, holdings
    period_from TEXT NOT NULL DEFAULT '', -- ISO dates; '' for a report of holdings only
    period_to   TEXT NOT NULL DEFAULT '',
    rows        INTEGER NOT NULL,
    imported_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, sha256)
);

-- Users whose trade events changed since their last reconcile.
CREATE TABLE IF NOT EXISTS finance_dirty (
    user_id TEXT PRIMARY KEY REFERENCES users(id),
    since   INTEGER NOT NULL -- unix nanoseconds of the latest change
);

-- Per-user settings. Profit in the base currency is converted at each
-- day's ECB rate.
CREATE TABLE IF NOT EXISTS finance_settings (
    user_id       TEXT PRIMARY KEY REFERENCES users(id),
    base_currency TEXT NOT NULL DEFAULT 'EUR'
);
