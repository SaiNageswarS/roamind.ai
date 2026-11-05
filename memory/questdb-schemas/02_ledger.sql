-- Each user's ledger. Every table has user_id (users.id in SQLite) as the
-- first upsert key after the timestamp, and only memory/finance reads or
-- writes them, binding the user as a parameter. Money and quantities are
-- DECIMAL(24, 8), prices of a unit DECIMAL(20, 6). Applied as
-- 01_market.sql is.

-- Every trade, dividend or charge as one source reported it, unchanged. A
-- source replayed writes the same rows, which dedup skips.
CREATE TABLE IF NOT EXISTS observations (
    ts           TIMESTAMP, -- the trade date
    user_id      SYMBOL,
    source       SYMBOL, -- mail:<address>/<message id>, upload:<sha256>, chat:<thread>
    line         INT,    -- its row in that source
    account      SYMBOL, -- fin_accounts.id
    isin         SYMBOL,
    side         SYMBOL, -- buy, sell, vest, espp, dividend, transfer_in, transfer_out
    quantity     DECIMAL(24, 8),
    price        DECIMAL(20, 6),
    amount       DECIMAL(24, 8),
    fees         DECIMAL(24, 8),
    tax_withheld DECIMAL(24, 8),
    currency     SYMBOL,
    fx_rate      DECIMAL(20, 10), -- the broker's, when given
    ref          VARCHAR,         -- order or trade number
    doc          SYMBOL, -- contract_note, confirmation, tradebook, statement, chat
    received_at  TIMESTAMP
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, user_id, source, line);
ALTER TABLE observations DEDUP ENABLE UPSERT KEYS(ts, user_id, source, line);

-- One row per day-group (user, account, isin, trade date, side), written by
-- finance-reconcile from the observations; an upsert replaces it. Entry and
-- exit points are these rows grouped by ISIN across accounts.
CREATE TABLE IF NOT EXISTS trades (
    ts           TIMESTAMP, -- the trade date
    user_id      SYMBOL,
    account      SYMBOL,
    isin         SYMBOL,
    side         SYMBOL,
    quantity     DECIMAL(24, 8),
    avg_price    DECIMAL(20, 6),
    amount       DECIMAL(24, 8),
    fees         DECIMAL(24, 8),
    tax_withheld DECIMAL(24, 8),
    currency     SYMBOL,
    record       SYMBOL, -- the source of record
    sources      INT,    -- how many families agreed
    state        SYMBOL  -- active, void
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, user_id, account, isin, side);
ALTER TABLE trades DEDUP ENABLE UPSERT KEYS(ts, user_id, account, isin, side);

-- Holdings a statement or CAS showed, to check the ledger against; they
-- never change the trades.
CREATE TABLE IF NOT EXISTS holdings_reported (
    ts       TIMESTAMP, -- the as-of date
    user_id  SYMBOL,
    account  SYMBOL,
    isin     SYMBOL,
    quantity DECIMAL(24, 8),
    value    DECIMAL(24, 8),
    currency SYMBOL,
    source   SYMBOL
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, user_id, account, isin);
ALTER TABLE holdings_reported DEDUP ENABLE UPSERT KEYS(ts, user_id, account, isin);

-- A daily snapshot by finance-daily, in the instrument's currency and the
-- user's base currency; dashboards and the weekly summary read this.
CREATE TABLE IF NOT EXISTS valuations (
    ts            TIMESTAMP, -- the day
    user_id       SYMBOL,
    account       SYMBOL,
    isin          SYMBOL,
    quantity      DECIMAL(24, 8),
    close         DECIMAL(20, 6),
    value         DECIMAL(24, 8),
    cost          DECIMAL(24, 8),
    currency      SYMBOL,
    value_base    DECIMAL(24, 8),
    cost_base     DECIMAL(24, 8),
    base_currency SYMBOL
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, user_id, account, isin);
ALTER TABLE valuations DEDUP ENABLE UPSERT KEYS(ts, user_id, account, isin);

CREATE TABLE IF NOT EXISTS subscription_charges (
    ts       TIMESTAMP, -- the charge date
    user_id  SYMBOL,
    key      SYMBOL, -- subscriptions.key
    source   SYMBOL,
    amount   DECIMAL(24, 8),
    currency SYMBOL
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, user_id, key, source);
ALTER TABLE subscription_charges DEDUP ENABLE UPSERT KEYS(ts, user_id, key, source);
