-- Saved queries for the QuestDB web console (https://questdb.<tailnet>.ts.net),
-- to paste in and chart. The console sees every user's rows: set the user's
-- id (users.id in data/roamind.db) in each query. Tables are in
-- memory/questdb-schemas.

-- Portfolio value over time, in the base currency.
SELECT ts, sum(value_base) AS value, sum(cost_base) AS cost
FROM valuations
WHERE user_id = '<user id>'
SAMPLE BY 1d ALIGN TO CALENDAR;

-- Allocation by market and currency, at the latest valuation.
SELECT i.market, v.currency, sum(v.value_base) AS value
FROM (valuations WHERE user_id = '<user id>' LATEST ON ts PARTITION BY account, isin) v
JOIN instruments i ON i.isin = v.isin
WHERE v.quantity > 0
ORDER BY value DESC;

-- Entry and exit points of one stock across accounts, against its close.
SELECT t.ts, t.side, t.account, t.quantity, t.avg_price, p.close
FROM trades t
LEFT JOIN prices p ON p.isin = t.isin AND p.ts = t.ts
WHERE t.user_id = '<user id>' AND t.isin = '<isin>' AND t.state = 'active'
ORDER BY t.ts;

-- One stock's closes, to chart beside its entry and exit points.
SELECT ts, close FROM prices WHERE isin = '<isin>' ORDER BY ts;

-- Trades a month, by side.
SELECT ts, side, count() AS trades, sum(amount) AS amount
FROM trades
WHERE user_id = '<user id>' AND state = 'active'
SAMPLE BY 1M ALIGN TO CALENDAR;

-- Subscriptions by month.
SELECT ts, currency, sum(amount) AS spent
FROM subscription_charges
WHERE user_id = '<user id>'
SAMPLE BY 1M ALIGN TO CALENDAR;

-- Day-groups confirmed by one source only, newest first: the ones to check.
SELECT ts, account, isin, side, quantity, record
FROM trades
WHERE user_id = '<user id>' AND state = 'active' AND sources = 1
ORDER BY ts DESC
LIMIT 50;
