-- Market data, shared by every user: fetched once for the union of their
-- holdings, so it has no user_id. Applied by `task init` (bin/worker
-- -questdb-init); every statement must be safe to repeat. Each table's
-- DEDUP UPSERT KEYS is restated after it, so a changed key list reaches a
-- table created before the change. Prices of a unit are DECIMAL(20, 6).

-- Reference data; ts is fixed at 1970-01-01, which makes the ISIN the key.
CREATE TABLE IF NOT EXISTS instruments (
    ts       TIMESTAMP,
    isin     SYMBOL,
    name     VARCHAR,
    kind     SYMBOL, -- stock, etf, fund
    market   SYMBOL, -- US, EU, IN
    currency SYMBOL,
    main_mic SYMBOL  -- the exchange whose close values it
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, isin);
ALTER TABLE instruments DEDUP ENABLE UPSERT KEYS(ts, isin);

-- Where an instrument trades; ts is fixed, as for instruments.
CREATE TABLE IF NOT EXISTS listings (
    ts       TIMESTAMP,
    isin     SYMBOL,
    mic      SYMBOL,
    symbol   VARCHAR, -- the EODHD code, or the AMFI scheme code of a fund
    currency SYMBOL
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, isin, mic);
ALTER TABLE listings DEDUP ENABLE UPSERT KEYS(ts, isin, mic);

-- End-of-day bars, unadjusted: splits are applied from corporate_actions.
CREATE TABLE IF NOT EXISTS prices (
    ts         TIMESTAMP, -- the trading day
    isin       SYMBOL,
    mic        SYMBOL,
    open       DECIMAL(20, 6),
    high       DECIMAL(20, 6),
    low        DECIMAL(20, 6),
    close      DECIMAL(20, 6),
    volume     LONG,
    fetched_at TIMESTAMP
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, isin, mic);
ALTER TABLE prices DEDUP ENABLE UPSERT KEYS(ts, isin, mic);

-- Daily rates, from the source each tax rule names.
CREATE TABLE IF NOT EXISTS fx (
    ts     TIMESTAMP, -- the day
    pair   SYMBOL,    -- e.g. EURUSD: 1 EUR in USD
    source SYMBOL,    -- ecb, sbi-tt
    rate   DECIMAL(20, 10)
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, pair, source);
ALTER TABLE fx DEDUP ENABLE UPSERT KEYS(ts, pair, source);

CREATE TABLE IF NOT EXISTS corporate_actions (
    ts       TIMESTAMP, -- the ex-date
    isin     SYMBOL,
    kind     SYMBOL, -- split, bonus, dividend, spinoff
    factor   DECIMAL(20, 10), -- new shares per old, for a split or bonus
    amount   DECIMAL(20, 6),  -- per share, for a dividend
    currency SYMBOL,
    source   SYMBOL
) TIMESTAMP(ts) PARTITION BY YEAR WAL
DEDUP UPSERT KEYS(ts, isin, kind);
ALTER TABLE corporate_actions DEDUP ENABLE UPSERT KEYS(ts, isin, kind);
