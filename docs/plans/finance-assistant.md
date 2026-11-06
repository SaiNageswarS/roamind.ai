# Finance assistant (plan)

Status: drafted 2026-11-04; revised 2026-11-05 around QuestDB (PR #19 added `task questdb`), several users, and a programmatic de-dupe of mail and uploaded reports.

## Context
Roamind should follow each user's investments across their accounts in the US, the EU and India, and alert them about the stocks they hold. It should give them their holdings, profit and tax estimates, and track their subscriptions, all in a channel of their own: `#finance-<name>`.

A user may hold shares, ETFs and mutual funds at several brokers across those markets, including employer stock plans (RSU vests, ESPP purchases), and may trade dozens of times a month.

Decided with the user:
- **Several users.** Every user's financial data is theirs alone, as their memory is. Market data is shared.
- **Two sources: mail and reports.** Trades come from broker mail, which the email assistant already reads. They also come from reports (tradebooks, statements, contract notes, CAS) that the user uploads in their finance channel. The same trade often arrives from both, and from two mails (an order confirmation, then a contract note). **De-duping is done by code**, not by a model.
- **QuestDB holds the time series:** prices, FX, corporate actions, trades and daily valuations. SQLite keeps the relational state, beside the rest of Roamind's.
- **QuestDB's console is a dashboard on the tailnet**, behind Caddy like Hatchet and the notebook.
- **What the user sees:**
  - one line per stock across accounts, with its entry and exit points (not each transaction);
  - realized and unrealized profit, dividends, and the overall total;
  - tax estimates by tax year;
  - a re-entry alert when a stock falls below the user's last exit price;
  - price, news, concentration and allocation alerts;
  - a weekly summary.

Findings that shape the design:
- **Brokers can't be read directly.**
  - Retail broker APIs, where they exist, mostly need a browser login every day: their access tokens expire at midnight.
  - Many brokers and neobanks have no API for holdings at all.
  - Mail and reports cover every account the same way.
- **QuestDB fits the de-dupe.** A WAL table can declare `DEDUP UPSERT KEYS(ts, …)`: a row with the same keys replaces the old one, and an identical row is skipped. That makes every write here an idempotent upsert: a retried run, or a report uploaded twice, changes nothing. The keys must include the designated timestamp.
- **QuestDB has `DECIMAL(precision, scale)`** since 9.2, with exact arithmetic and no implicit conversion from doubles, so money is stored exactly. The `task questdb` install is 10.1.0.
- **QuestDB isn't a transactional store.**
  - It has no foreign keys and no unique constraints beyond dedup keys.
  - It has no transactions across tables.
  - WAL writes are applied asynchronously, so a read straight after a write may not see it.
  - So it holds facts keyed by time. Mutable state (accounts, rules, subscriptions, uploads) stays in SQLite. Reads that must see their own writes first wait for the WAL to be applied (section 5).
- **QuestDB's open-source build has no row-level security or roles.** Each user's isolation is enforced in Roamind's code: every per-user table has a `user_id` column, and only the store package queries it. The console sees everything, so it is for the operator only.
- **The tailnet is trusted.** The console and QWP share port 9000 and QuestDB asks for no login there, so the tailnet and Caddy's login check are the gates. QuestDB's `http.security.readonly` would make the console query-only, but it refuses QWP writes too.

  Each maps to a `QDB_…` environment variable.
- **Market data:**
  - EODHD gives end-of-day prices for US, EU and Indian exchanges, plus dividends, splits and news by ticker. Its Historian plan is £16.66 a month billed yearly.
  - The ECB publishes free daily euro reference rates.
  - AMFI publishes Indian mutual fund NAVs daily.
  - OpenFIGI maps an ISIN to its listings.
- **Industry practice for prices:**
  - Store unadjusted prices, with corporate actions kept separately, and adjust when reading (CRSP's method).
  - Key bars by the exchange's own trading day.
  - Identify a security by its ISIN (ISO 6166) and an exchange by its MIC (ISO 10383).

## Flow

```
broker mail ─▶ Apps Script (finance) ─▶ mail-message: check ─▶ extract (small model) ─▶ record  ┐
report upload in #finance-<name> ─▶ chat turn ─▶ import_report (parser, or model rows) ─▶ record  ┤
chat ("I sold 20 INFY") ─▶ chat turn ─▶ record_trade ─────────────────────────────────▶ record  ┤
                                                                                                 ▼
                              QuestDB trade_events (raw, one row per source line, dedup on source+line)
                                                                                                 │ marks the user dirty
                                                                                                 ▼
                      finance-reconcile (per user, serialized): the same fill from any two sources counts once → trades
                                                                                                 ▼
finance-daily: prices, FX, actions ─▶ valuations ─▶ rules in Go ─▶ alert turn ─▶ #finance-<name>
finance-weekly: figures from QuestDB ─▶ summary turn ─▶ #finance-<name>
QuestDB console ◀── Caddy questdb.<tailnet>.ts.net ◀── operator
```

## Design

### 1. QuestDB as a service
- **process-compose:**
  - QuestDB moves into process-compose as `questdb`, running `questdb.sh start -n -d data/questdb`; `-n` keeps it in the foreground.
  - Its readiness probe is the health port's `/status`.
  - `init` and the `worker` wait for it.
  - `task questdb` stays for a manual start without the rest of the stack.
- **Ports, all on loopback** (as now):
  - 9000: QWP, the console and REST API;
  - 9003: health.
- **What gets turned off:** Postgres wire (`QDB_PG_ENABLED=false`), line protocol over TCP and UDP, and telemetry. Line protocol over HTTP stays on, because QWP needs it.
- **The Go client** is QuestDB's official one, `github.com/questdb/go-questdb-client/v4`, over QWP. Writes are `INSERT`s run with `Exec`, which fails on a refused write; the client's ingestion sender would report a refusal only later, in the background. The volume is tiny (~300 trades a user a year, ~15k price rows a year).
- **Schemas** are `CREATE TABLE IF NOT EXISTS` files in `memory/questdb-schemas/*.sql`, applied by `task init` with the worker's binary (`bin/worker -questdb-init`). A changed dedup key list is applied with `ALTER TABLE … DEDUP ENABLE UPSERT KEYS`.

### 2. Data
Shared tables have no `user_id`; every per-user table has one, as the first upsert key after the timestamp. Money and quantities are `DECIMAL(24, 8)`; prices of a unit are `DECIMAL(20, 6)`. Every QuestDB table is a WAL table.

**QuestDB: shared market data**

| Table | Timestamp | Upsert keys | Columns |
|---|---|---|---|
| `instruments` | `ts` fixed at 1970-01-01 | `ts, isin` | `name`, `kind` (stock, etf, fund), `market` (US, EU, IN), `currency`, `main_mic`. Reference data; the fixed timestamp makes the ISIN its key. |
| `listings` | `ts` fixed | `ts, isin, mic` | `symbol` (EODHD code or AMFI scheme), `currency` |
| `prices` | trading day, `PARTITION BY YEAR` | `ts, isin, mic` | unadjusted `open`, `high`, `low`, `close`, `volume`, `fetched_at` |
| `fx` | day | `ts, pair, source` | `rate`; `source` is `ecb` or `sbi-tt`, the rates each tax rule names |
| `corporate_actions` | ex-date | `ts, isin, kind` | `kind` (split, bonus, dividend, spinoff), `factor`, `amount`, `currency`, `source` |

**QuestDB: per user**

| Table | Timestamp | Upsert keys | What it holds |
|---|---|---|---|
| `trade_events` | trade date | `ts, user_id, source, line` | Every trade, dividend or charge as one source reported it, unchanged. `source` is `mail:<address>/<message id>`, `upload:<sha256>` or `chat:<thread>`; `line` is its row in that source. Columns: `account`, `isin`, `side` (buy, sell, vest, espp, dividend, transfer_in, transfer_out), `quantity`, `price`, `amount`, `fees`, `tax_withheld`, `currency`, `fx_rate` (the broker's, when given), `ref` (order or trade number), `doc` (contract_note, confirmation, tradebook, statement, chat), `received_at`. A source replayed writes the same rows, which dedup skips. |
| `trades` | trade date | `ts, user_id, account, isin, side` | The reconciled day-group (section 4): `quantity`, `avg_price`, `amount`, `fees`, `tax_withheld`, `currency`, `record` (the source of the copy kept). Entry and exit points are these rows grouped by ISIN across accounts. |
| `valuations` | day | `ts, user_id, account, isin` | Written daily by `finance-daily`: `quantity`, `close`, `value`, `cost`, both in the instrument's currency and in the base currency. Dashboards and the weekly summary read this, not a recomputation. |
| `subscription_charges` | charge date | `ts, user_id, key, source` | `amount`, `currency` |

Positions, average cost, realized gains (oldest-first per account), entry and exit points and tax are computed in Go from `trades`, `corporate_actions` and `fx` (`memory/finance`). They're never stored, except the daily `valuations` snapshot.

**SQLite (`memory/sql-schemas/12_finance.sql`): state, per user**
- `fin_accounts(user_id, id, broker, number, currency, name, PK(user_id, id))`:
  - An account is created on its first trade event, from the broker and the masked account number in the mail or report.
  - The user names it in chat.
  - The `(user_id, broker, number)` pair is unique, which is how a mail or report is assigned to an account.
- `finance_uploads(user_id, sha256, file, broker, account, doc, period_from, period_to, rows, imported_at, PK(user_id, sha256))`: the reports imported, with the period each covers.
- `finance_dirty(user_id PK, since)`: users whose trade events changed since their last reconcile.
- `alert_rules`:
  - columns: `(user_id, id, kind, isin, listing, scope, direction, threshold, margin, target, expires_on, state)`;
  - `kind` is `move`, `level`, `drawdown`, `concentration`, `allocation` or `reentry`.
- `alert_state(user_id, rule, isin, firing, since)`: an alert fires when `firing` flips on.
- `subscriptions(user_id, key, amount, currency, cadence, next_charge, cancel_by, paid_with, state, source)`.
- `tax_residence(user_id, year, country)`: asked for on the user's first tax question.
- `finance_posts(user_id, kind, day)`: one daily and one weekly post at most.
- `users.base_currency` (default EUR), and `KindFinance` in `channels`.

### 3. Getting the data in
Both sources produce the same thing: a list of trade events with a source, a document kind and line numbers. Code then checks the rows; the model never decides what is a duplicate.

**Mail** (extends the email assistant):
- The Apps Script gets a `finance` category: contract notes, trade, vest and ESPP confirmations, dividends, corporate actions, statements. For `finance` mail it also sends the text of PDF attachments, up to 20k characters, through Drive's OCR. A password-protected PDF is sent by name only.
- `mail-message` gets a step, `extract`, for `finance` mail, in place of `relate`. It uses a small model (Haiku, or the script's DeepSeek), with a JSON schema for trade events, since trades need no memory. A trade costs a fraction of a cent, not the $0.06–0.12 of a `relate` turn.
- `save` checks the rows, writes them to `trade_events` (source `mail:<address>/<id>`, `line` its index), marks the user dirty and starts `finance-reconcile`.
- Rows that fail the checks go to `relate`, so the user hears about them. The checks:
  - the ISIN checksum;
  - quantity × price + fees within 1% of the amount;
  - the date within the mail's week.
- A one-off `backfill(fromDate)` in the script sends past broker mail. It goes through the same path, and dedup makes a second run harmless.

**Reports uploaded in `#finance-<name>`:**
- Files shared in the channel are downloaded into the workspace's `uploads/`, as in every channel. The finance turn calls `import_report(file, broker, account, doc, period_from, period_to, rows?)`.
- The tool, not the model:
  - hashes the file;
  - stops if `finance_uploads` has the hash for this user ("already imported on 3 Nov");
  - parses it.
- **Parsers come first.** Known CSV and XLSX formats have Go parsers in `connectors/statements`: one per broker export: a tradebook, a transaction or holdings statement, a stock plan's gains and benefit history. Each parser is added when the first real file of that format is seen.
- **The model is the fallback**, for PDFs and unknown layouts. It reads the file and passes `rows` in the trade event schema, and the tool runs the same checks as for mail. A report's own totals, when it has them, must match the sum of its rows.
- A CAS PDF is encrypted with the PAN. The turn asks for the password in the thread and decrypts it in the sandbox with `qpdf`, which is added to the sandbox image. The password isn't stored.
- Rows go to `trade_events` with source `upload:<sha256>`. The tool marks the user dirty, and the worker starts `finance-reconcile` after the turn, the way `goalChannelsTrigger` runs. The MCP server has no Hatchet client.

**Chat:** `record_trade` writes one trade event with source `chat:<thread>`. It counts like mail, below it when both have the same fill.

### 4. De-dupe: `finance-reconcile` (`memory/finance/reconcile.go`, pure Go)
**Duplicates of the same input** never reach the reconcile. Each is stopped where it enters:

| Duplicate | Stopped by |
|---|---|
| The same mail sent again | `mail_messages` (as today), then the `trade_events` upsert key `(source, line)` |
| The same report uploaded again | its SHA-256 in `finance_uploads` |
| A retried run or tool call | the same upsert keys: identical rows are skipped |

**The same fill from two sources counts once.** A trade can reach the ledger from a report, from mail and from chat; the reconcile only makes sure it isn't counted twice. Nothing is deleted or voided, and there's no state per trade. Each trade is settled per day-group G = `(user, account, isin, trade date, side)`, the unit the user sees as an entry or exit point:
1. **Merge copies of one fill** across sources:
   - rows with the same `ref` are one fill;
   - rows without a `ref` are one fill when quantity is equal and price is within 0.1% (for dividends, the amount within 0.1%);
   - rows of one source are never copies of each other: a report's two alike rows are two executions.
2. **Keep the best copy:** an uploaded report's, then mail's by document (a contract note over an order confirmation), then chat's.
3. **Write G** to `trades`, summed over its fills; the upsert replaces it.

This is how a contract-note mail and the tradebook row for the same order count once, while two real orders of the same day both count. A fill only one source has is a fill.

**Running it:**
- **Serialized:** `finance-reconcile` is a Hatchet task per user, concurrency key `user_id`, so a user's groups are never reconciled twice at once.
- **Order-independent:** its output depends only on the trade events, so runs in any order converge.
- **Waiting for writes:** before reading, it waits until `wal_tables()` shows `trade_events` applied up to the latest transaction, with a timeout and a retry.
- **What triggers it:** mail `save`, an upload turn, and `finance-daily` for any user left dirty.

### 5. Several users
- **Tables:**
  - every per-user QuestDB table and SQLite table has `user_id`;
  - market data is shared and fetched once for the union of every user's holdings, so EODHD calls grow with the listings held, not with users.
- **One way in:**
  - Only `memory/finance` queries QuestDB, and every function takes the user id and binds it as a parameter.
  - The agent never writes SQL. Its tools are bound to the turn's user, as the `items` tools are.
  - A store test writes two users' data and checks each function returns only its user's rows.
- **Accounts:**
  - Mail is assigned to a user by its `mail_accounts` row (as today), and to an account by broker and account number within that user.
  - An upload belongs to the channel's owner; only the owner's messages in `#finance-<name>` are finance turns.
- **Deleting a user's finances:** QuestDB has no `DELETE`. `task finance-forget -- <email>` rewrites each per-user table without the user (`CREATE TABLE … AS SELECT … WHERE user_id != $1`, then a rename) and deletes their SQLite rows. It's rare, so a rewrite is fine.
- **The operator sees all users' finances** in the console. The docs say so, as they do for SQLite and memory today.

### 6. The channel and tools
- `#finance-<name>` (`KindFinance`) is created when it's first needed, not by user-setup:
  - on the user's first finance question anywhere;
  - or when the first finance mail arrives for them.

  It's made by a `finance-channel` task with `openChannel`. Its intro says what it tracks and that reports can be uploaded there.
- The owner's plain messages are finance turns. They get the `finance` tool set and a prompt paragraph naming their accounts.
- **Tools** (`mcp/tools/finance.go`):
  - `list_holdings`: one line per stock across accounts, with quantity per account, average cost, value, unrealized and realized gain, dividends, total, and the last few entry and exit points;
  - `stock_history`: all of a stock's entry and exit points;
  - `profit`: totals by stock, market and year;
  - `tax_estimate`: by tax year and country, with "tax if sold now" and the days until an Indian holding becomes long-term;
  - `import_report`, `record_trade`;
  - `list_alerts`, `set_alert`, `mute_alert`;
  - `list_subscriptions`, `save_subscription`, `cancel_subscription`;
  - `set_tax_residence`, `set_base_currency`.

  All figures come from the tools; the model never computes them.

### 7. Market data: `connectors/market`
- **EODHD**, with `EODHD_API_KEY`:
  - end-of-day bars (bulk per exchange when several listings are held there);
  - dividends and splits, written to `corporate_actions`;
  - news by ticker.
- **ECB** euro reference rates, and the SBI TT rate where Indian rules name it.
- **AMFI** NAVs, for mutual funds.
- **OpenFIGI**, to resolve a new ISIN to its listings.
- Requests go through `connectors/safehttp`. Only tickers and ISINs are sent, nothing about a user.

### 8. Alerts, summary, re-entry
- **`finance-daily`:**
  - It runs at 07:00 in `ROAMIND_TZ`, Tuesday to Saturday, after all three markets' closes.
  - Its tasks run in order:
    1. fetch prices, FX and corporate actions;
    2. reconcile users left dirty; a split becomes trades;
    3. write `valuations`;
    4. check every user's rules in Go;
    5. draft one alert turn per user with anything triggered;
    6. post.
  - Quiet days cost no turn.
- **Re-entry:** every exit sets the stock's `reentry` rule to the exit price, in the currency of the listing it was sold on. A later exit moves it, and a buy clears it.
  - It fires once, on the first close below the exit price less a margin (default 2%), split-adjusted through `corporate_actions`, then again only after the price has recovered above it.
  - It expires after 90 days.
  - A day's re-entries go in one message, ordered by how far each stock has fallen.
- **Other rules:** a big daily move, a price level, a fall below cost, concentration, and allocation by market or currency.
- **News:** news on held stocks, kept when material (results, corporate actions, filings, management, regulators).
- **`finance-weekly`**, Monday 07:00:
  - value by account, market and currency, and the week's change;
  - realized and unrealized profit, dividends, and the tax estimate so far this year;
  - the top movers, and concentration against the user's targets;
  - the subscriptions' monthly total and renewals in the next 30 days.
- **Subscriptions** come from mail (`subscription` category, through `relate`) and from bank or card statements uploaded in the channel. Charges de-dupe like trades: by upsert key, then by `(key, charge date ± 2 days, amount)` across sources.

### 9. Tax
- Each sale is matched oldest-first per account, which both German and Indian rules use. Each sale is tagged with its tax year, its holding period and the residence country of that year (`tax_residence`).
- **Germany:**
  - 25% + 5.5% solidarity surcharge, plus church tax where it applies;
  - €1,000 a year tax-free;
  - stock losses offset only stock gains;
  - equity ETFs are 30% exempt;
  - foreign withholding is credited up to 15%;
  - EUR amounts use each transaction day's ECB rate.
- **India:**
  - the April–March year;
  - listed equity held over 12 months: 12.5% above ₹1.25 lakh; held under 12 months: 20%;
  - tax deducted at source for NRI accounts.
- The rates live in one table in `memory/finance/tax.go`, with the year they apply from. They are checked against the law before the build.
- Outputs are estimates for planning, not filing; which country taxes a gain under a treaty is left to the user's adviser.

### 10. Dashboards on the tailnet
- **Caddy:** a new block in `deploy/caddy/Caddyfile`:

  ```
  # QuestDB console → https://questdb.<tailnet>.ts.net
  :443 {
  	bind tailscale/questdb
  	import tailnet_owner_only
  	reverse_proxy 127.0.0.1:9000
  }
  ```

  Only `ROAMIND_DASHBOARD_USERS` get in, as for Hatchet and the notebook. The new device needs a `TS_AUTHKEY` or an approval on its first run (`docs/deployment.md`).
- **The gate is Caddy's login check,** on a trusted tailnet. A console session can change and drop data. Port 9000 stays on loopback, so the console is reached only through Caddy.
- **What's in the console:** a SQL editor that charts results, not saved multi-panel dashboards. Saved, ready-made queries go in `deploy/questdb/dashboards.sql`, to paste in:
  - portfolio value over time (from `valuations`);
  - allocation by market and currency;
  - realized gain by tax year;
  - one stock's entry and exit points against its price;
  - subscriptions by month.
- **Per-user dashboards are out of scope:** the console sees every user. If they're wanted later, Grafana with a per-user filter would be the next step.
- **The notebook** can query port 9000 too (`/exec`), for analysis in pandas.

### 11. Docs, config
- `docs/architecture.md`: a "Finance assistant" section with the flow, the de-dupe rules and the tenancy rules.
- `docs/deployment.md`:
  - QuestDB in process-compose;
  - its passwords;
  - the `questdb` tailnet device;
  - the EODHD key;
  - `qpdf`;
  - `task finance-forget`.
- `.env.example`: `ROAMIND_QUESTDB_ADDR`, `EODHD_API_KEY`.
- `README.md`: a feature line, and QuestDB on the dashboards list.
- **Privacy:**
  - Holdings, trades and subscriptions stay in `data/questdb` and `data/roamind.db`.
  - Market-data providers see only tickers and ISINs.
  - Uploaded reports stay in the user's workspace.
  - Figures in a post go to Claude in that turn.
  - The operator can read every user's finances in the console.

## Phases
Each phase is one branch and one PR.
1. **QuestDB as a service:** process-compose, the config, the schemas, `memory/finance` with the official client, and the console on the tailnet.
2. **Ledger from uploads:** accounts, `import_report` with the first parsers, the reconcile, prices and FX, the channel, and `list_holdings`, `stock_history` and `profit`.
   As built:
   - **No parsers yet:** the model reads every report and passes its rows, which the tool checks. A parser is added when a real export of a format is at hand.
   - **The base currency** is in `finance_settings`, not a column of `users`: `task init` reapplies every schema file, and `ALTER TABLE … ADD COLUMN` can't be repeated.
   - **The reconcile only de-dupes:** no confirmed, provisional or void state, no notices, no holdings checks or conflicts, and no `holdings_reported`.
   - **The channel** opens after a turn once the user has an account, rather than on their first finance question.
   - **The tool reconciles at once,** so the turn's answer sees its import; `finance-reconcile` catches up on anyone left dirty.
3. **Ledger from mail:** the `finance` category, `extract`, PDF text, and `backfill()`.
4. **Alerts:** `finance-daily`, `finance-weekly`, re-entry and the other rules, news, and subscriptions.
5. **Tax:** residence, the rules per country, `tax_estimate`, and the year-end report.

## Considered

### Everything in SQLite
At this volume SQLite would do. QuestDB was chosen by the user. It gives dedup upserts on every write, time-partitioned price history, and a console to look at the data. SQLite still holds what needs transactions and constraints.

### Trades in SQLite, mirrored to QuestDB
That would give transactions and unique constraints for the ledger, at the cost of two copies of every trade kept in step. The reconcile is serialized per user and is a pure function of the trade events, and dedup upserts make each write idempotent. So QuestDB can hold the ledger alone.

### A table per user in QuestDB
Isolation and deletion would be simpler (`DROP TABLE`), but every dashboard query and every shared join would be built per user, and the table count would grow with users. Shared tables with `user_id`, behind one store package, were chosen.

### A model deciding duplicates
A turn could compare a report with the mail it already saw. It would cost a turn per upload, could miscount, and its answer couldn't be tested. Code decides; the model only extracts rows from documents code can't parse.

### Postgres wire with pgx
Postgres wire would let the console stay read-only, with writes behind a password on a port the tailnet never sees. The tailnet is trusted, so QuestDB's official client was chosen, over one port.

### The client's ingestion sender
It's faster and buffers rows on disk, but it reports a refused write only later, in the background. Writes here are a few rows, which must be applied before the reconcile reads them, so they're `INSERT`s with `Exec`.

## Files
New:
- `memory/questdb-schemas/*.sql`, `memory/sql-schemas/12_finance.sql`
- `memory/finance/{questdb.go,ledger.go,reconcile.go,tax.go}` and tests
- `connectors/market/{eodhd.go,ecb.go,amfi.go,openfigi.go}`, `connectors/statements/*` and tests
- `mcp/tools/finance.go` and its test
- `agent/finance.go`, `agent/prompts/{extract_finance.md,finance_alert.md,finance_weekly.md}`
- `worker/workflows/{finance_reconcile.go,finance_daily.go,finance_weekly.go,finance_channel.go}` and tests
- `deploy/questdb/dashboards.sql`

Changed:
- `process-compose.yaml` (`questdb`), `Taskfile.yml` (`init`, `finance-forget`), `deploy/caddy/Caddyfile` (the `questdb` device)
- `memory/store/channels.go` (`KindFinance`), `worker/workflows/{chat_turn.go,turn_tools.go,workflows.go,mail_message.go}`
- `deploy/apps-script/Code.js` (`finance`, PDF text, `backfill`), `agent/mail.go`, `agent/prompts/system.md`
- the sandbox image (`qpdf`), `docs/*`, `README.md`, `.env.example`

New Go dependency: `github.com/questdb/go-questdb-client/v4` as a direct dependency of `memory`.

## Verification
1. **Unit tests:**
   - The reconcile, on fixed trade events:
     - a confirmation and a contract note for one order count once;
     - two orders of one day each count;
     - a report row and the mail of the same fill count once, and the report's copy is kept;
     - a fill only mail has is kept;
     - trade events in any order give the same trades.
   - Ledger maths: oldest-first matching per account, splits, transfers keeping cost, dividends with withholding, profit in the instrument's currency and in EUR.
   - Tenancy: two users' data, each function returning only its user's rows.
   - Parsers, on redacted real files.
   - Rule checks, including re-entry firing once and again after recovery, and split adjustment.
2. **QuestDB:**
   - Writing the same rows twice leaves one copy (`DEDUP`).
   - `bin/worker -questdb-init` is safe to repeat.
   - A refused write fails the call that made it.
   - `https://questdb.<tailnet>.ts.net` opens for a listed login, and gives 403 for others.
3. **Live, phase 2:**
   - Upload a broker's tradebook and compare `list_holdings` with the app.
   - Upload it again: "already imported".
   - Upload an overlapping statement: no doubled trades.
4. **Live, phase 3:**
   - Backfill one account's mail over a month that a report already covers: no new trades.
   - Mail for a day after the last report gives a trade at once.
   - The next contract note updates the holding.
5. **Live, phase 4:** a `level` alert just below a held stock's price fires once. After a real exit, the re-entry rule shows in `list_alerts`. The Monday summary matches the brokers' apps within FX.
