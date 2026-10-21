# Email assistant (plan, not built)

Status: drafted 2026-10-21 and revised 2026-10-22, to revisit. The `channels` table it relies on is live (PR #6, deployed 2026-10-21): the inbox channel is one more kind in it, so this plan adds no channel table. The 2026-10-22 revision made three changes:
- Item state (whether it is due, and whether it is still open) moved out of the notes and into an `items` table, and `status` was removed from Task (see "Why an items table").
- The planned Bill, Order and Checklist schemas were folded into the existing Task, Event and Preference schemas (see section 6), so the plan adds no schema.
- The automatic return-window reminders were dropped.

## Context
Roamind should read the user's Gmail (several accounts per user) and turn mail into structured Basic Memory notes: tasks, reminders, events/appointments, bills and renewals, travel bookings, orders and deliveries, people, and checklists of things to carry. A morning digest in a private `#inbox-<name>` Slack channel then shows what is due. This also delivers the roadmap's pending "reminders" item. Reminders are not a type of their own: they are rows of the `items` table, added for tasks and events, including tasks saved from chat.

Decided with the user:
- Connecting accounts is **offline and manual for now** (an operator task).
- New mail is **polled by a Hatchet cron**.
- Due items go to a **morning digest in `#inbox-<name>`**.
- Mail is captured for **bills & renewals, travel & bookings, orders & deliveries, people & contacts, appointments**, plus checklists.
- **Active items are tracked in SQLite**, and the details stay in Basic Memory.
- **No new schemas**:
  - a bill is a Task;
  - a booking, delivery or renewal is an Event;
  - a checklist is a field of its Event;
  - a reusable packing list is a Preference.

Research findings that shape the design:
- **Scope and app status.** `gmail.readonly` is a *restricted* scope. A personal, unverified app is allowed up to 100 users behind a click-through warning. The OAuth consent screen must be **"In production"**: in "Testing" mode, refresh tokens expire after 7 days. Each client/user pair has a cap on refresh tokens (about 50–100), so reconnecting many times silently revokes the oldest token.
- **Sync model** (Gmail sync guide). Do a full sync with `messages.list` and store the latest `historyId`. After that, call `history.list` from the stored id. A 404 means the history has expired (it is kept for about a week or more), so fall back to a full sync. Pub/Sub only replaces the trigger, and polling is enough here.
- **schema.org markup.** Many senders embed schema.org JSON-LD in their mail (Gmail markup): FlightReservation, LodgingReservation, EventReservation, ParcelDelivery, Order, Invoice. Pass it to extraction as-is; it is far more reliable than prose.
- **Prior art.** Inbox Zero (AGPL) does triage, not extraction into a personal knowledge base. Nothing to reuse.
- **Basic Memory can't answer "what is due".** bm's metadata filters (`search_notes --filter` with `$gte`/`$lte`) see only frontmatter. In our schemas a field is an observation category, so `due` is a `- [due] <date>` line in the note body, which no bm query can range over. The alternative is to scan the note files on disk and parse those lines.

## Why an items table
Item state lives in SQLite, in `items` rows, and Basic Memory keeps everything else.
- **The table answers "what is due today" with one indexed query.** The digest doesn't scan notes, and its cost doesn't grow with years of history.
- **The table is the only owner of state.** It holds when an item shows, when it expires, and whether it is open, done or cancelled. Task and Event notes have no `status` field. Because state is stored in one place, a note and a row can't disagree about it.
- **Every write to state goes through code that writes the row:**
  - mail `save` (harness code), which writes the note and the rows from the same extracted item;
  - the `items` tools of Roamind's MCP server, in chat.
- **Basic Memory keeps the details and the links:** the booking's place and confirmation, the amount of a bill, the tracking link, the dates, the people, the checklist, the related goal. The digest turn reads them from there.

## Design

### 1. Connecting an account (offline, operator)
- **One-time Google Cloud setup** (documented in `docs/deployment.md`): enable the Gmail API, set up the consent screen as External and *In production*, and create a **Desktop** OAuth client. Save it as `data/secrets/google-oauth-client.json`.
- **`task mail-connect -- <roamind user email>`** (Taskfile, shell, standard tool **oauth2l**, plus a `task oauth2l` install task):
  1. Run `oauth2l fetch --credentials data/secrets/google-oauth-client.json --scope https://www.googleapis.com/auth/gmail.readonly --output_format refresh_token --cache ""`. This opens the browser through the loopback flow, and the operator picks the Gmail account. The output is an `authorized_user` JSON holding the client id, client secret and refresh token.
  2. Get the account address with `curl` to `gmail/v1/users/me/profile`, using an access token from the same credentials.
  3. Save the credentials as `data/secrets/mail/<address>.json` (chmod 600). Then `sqlite3 INSERT OR REPLACE` into `mail_accounts`, with `user_id = sha256(lower(trim(email)))`. The task refuses if no such user exists.
  4. Run it again for each further account. A user may have many accounts; an address belongs to one user.
- **`task mail-disconnect -- <address>`** deletes the row and the credentials file.
- The worker turns the file into a token source with `golang.org/x/oauth2/google.CredentialsFromJSON`, so Roamind has no OAuth code of its own. When a refresh fails (`invalid_grant`), the account is marked `failed` with the error, and the digest says it needs reconnecting.

### 2. Data
**Mail: `memory/sql-schemas/09_mail.sql` + `memory/store/mail.go` (+ `mail_test.go`)**
- `mail_accounts(address PK, user_id, history_id, status active|failed, error, connected_at, synced_at)`
- `mail_messages(address, message_id, user_id, status picked|done|skipped|failed, received_at, done_at, PK(address, message_id))`. Every message seen is recorded before it is read, so none is extracted twice. This follows the `job_postings` pattern.
- **The inbox channel is a row of the existing `channels` table** (`memory/sql-schemas/08_channels.sql`, `memory/store/channels.go`), with `kind = 'inbox'` and `ref = ''`. Each user has one inbox channel, and the empty `ref` in the primary key `(user_id, kind, ref)` enforces that, as for news.
  - Add `store.KindInbox = "inbox"` next to `KindGoal` and `KindNews`, and add `inbox` to the `kind` comment in `08_channels.sql`. `kind` is free text, so the table doesn't change.
  - Lookups use the existing API: `Store.Channel(ctx, userID, store.KindInbox, "")` for a user's channel, and `Store.Channels(ctx, store.KindInbox)` for every user's.
  - An inbox channel can't also be a user's goal or news channel, because of `UNIQUE (channel_id, user_id)`.
- `inbox_digests(user_id, day, channel_id, ts, posted_at, UNIQUE(user_id, day))`. The slot is reserved, then the message posted, then the slot completed, as `digests` does.
- Store functions keep the repo's short names: `MailAccounts`, `SetHistory`, `FailAccount`, `PickMessages`, `PendingMessages`, `FinishMessage`, `ReserveInbox`, `CompleteInbox`.

**Items: `memory/sql-schemas/10_items.sql` + `memory/store/items.go` (+ `items_test.go`)**

These are kept separate from mail because chat adds items too.
```sql
items(user_id, key,       -- key: stable title, or the confirmation/order/invoice number
      kind,               -- the note type: task|event
      event_kind,         -- the Event's kind (delivery, travel…), which sets its lead time; '' for tasks
      note,               -- permalink of the bm note it comes from
      title,              -- one line for the digest
      date,               -- the task's due or the event's date; NULL if undated
      show_from,          -- first day it shows: remind_on, or date minus the lead time; NULL never shows
      until,              -- last day it shows; NULL = until it is closed
      state,              -- open|done|cancelled
      source,             -- 'mail:<address>/<message id>' or 'chat'
      updated_at, closed_at,
      PRIMARY KEY (user_id, key),
      INDEX (user_id, state, show_from))
```
- **Every task and event note has a row, because its state is kept there.** An undated task gets a row with `date` and `show_from` both NULL. It never shows in the digest, and the row still records whether it is open or done.
- **What mail turns into:**

  | Mail | Note | Row |
  |---|---|---|
  | A bill or invoice to pay | Task "Pay \<payee> \<invoice number>", with `due` and `amount` | `remind_on` is `due` minus 3 days, set by extraction; the row closes as done when paid |
  | A subscription or renewal that is charged automatically | Event, `kind: renewal` | shows the day before, so it can still be cancelled |
  | An order | Event, `kind: delivery`, dated by the expected delivery, with the tracking `link` | closes as done when delivered, or as cancelled |
  | A flight, train, stay, ticket or appointment | Event with its `kind`, `confirmation` and `checklist` | closes as cancelled when cancelled |
  | A commitment ("I'll send it by Friday") | Task | |
- **Return windows get no reminder.** Most orders aren't returned, so one reminder per order would be mostly noise. The delivery event's body records the window ("returnable until 2026-11-20"). When the user says they want to return something, the agent creates a task due that day.
- **Closed rows are kept as history.** They answer "was that order delivered?" or "did I pay the insurance?", together with `closed_at`.
- **Lead times and expiry** are set in the harness (`InboxLeads`), so `show_from` and `until` are computed once, when the row is written:

  | kind | shows from | until |
  |---|---|---|
  | task | `remind_on`, else `due` minus 1 day | NULL: overdue tasks (and unpaid bills) stay until done |
  | event, `kind: delivery` | `date` | `date` |
  | any other event | `date` minus 1 day | `end`, else `date` |
- **Store functions:**
  - `PutItem` upserts by key. A later mail that reschedules an item moves its dates, and one that cancels it sets its `state`.
  - `CloseItem(userID, key, state)` sets `closed_at`. It also reopens an item when `state` is `open`.
  - `DueItems(userID, day)` returns `state = 'open' AND show_from <= day AND (until IS NULL OR until >= day)`.
  - `Items(userID, filter)` serves chat. The filter can select by state, by a `date` range, by key, or by note folder (such as `tasks/<goal>/` for a goal's tasks).

### 3. Connector: `connectors/mail` (Gmail, `google.golang.org/api/gmail/v1`)
- `Open(ctx, credPath) (*Account, error)`, `Profile`, `Changes(ctx, historyID) (ids, newHistoryID, error)` (returns `ErrHistoryGone` on 404), `Recent(ctx, days, max)` (the full sync: `newer_than:14d`, at most 50 messages), and `Message(ctx, id) (*Message, error)`.
- A `Message` holds `ID, ThreadID, From, To, Cc, Date, Subject, Labels, Text, JSONLD []string, Attachments []string` (attachment names only).
  - `Text` is taken from text/plain when present, otherwise from HTML converted to text with `golang.org/x/net/html`. It is capped at about 20k characters, with quoted replies removed.
  - `JSONLD` holds the `application/ld+json` scripts of the HTML part.
- **Pre-filter** (no LLM): skip `SPAM`, `TRASH`, `CATEGORY_PROMOTIONS`, `CATEGORY_SOCIAL` and `CATEGORY_FORUMS`. Keep `INBOX` and also `SENT`, because the user's own replies hold commitments ("I'll send it by Friday").
- Tests use testdata Gmail API JSON: multipart, HTML-only, a flight with JSON-LD, and a 404 history.

### 4. Workflows (`worker/workflows/`)
- **`mail-sync`** (cron `*/15 * * * *`): for each `active` account, starts a `mail-account` child run. Its output lists accounts whose run couldn't start, as `news-shorts` does.
- **`mail-account`** DAG, serialized per user (concurrency key `user_id`, so one user's bm writes never overlap):
  1. **`fetch`**: open the account and call `Changes` from `history_id`. With no history id, or on `ErrHistoryGone`, call `Recent`. Apply the pre-filter and `PickMessages` (new ids only), *then* save the new `history_id`. Return up to 20 picked messages, oldest first, read with `Message`. Leftovers stay `picked` for the next run. On an auth failure, call `FailAccount`.
  2. **`extract`**: one `ToolsNone` turn over the batch (`agent/prompts/extract_mail.md`), with structured output. The mail is third-party text, so the turn gets no tools and no memory, as news ranking and job tailoring do.
     - Input: the user's name, email, timezone and today's date; their active goals (title and permalink, from `notes.Goals`) so tasks can be linked; and each message's headers, text and JSON-LD.
     - Output: per message, either `skip` with a reason (newsletter, notification, OTP or security code) or a list of items.
       - Each item has a `kind` (task, event, person), the fields of that schema, a `key` and a `state`.
       - The key is a stable title: a confirmation, order or invoice number when there is one.
       - Tasks can carry a `remind_on`. Bills always do.
       - Events carry their `checklist` lines.
     - Retried once.
  3. **`save`**: for each item, the harness does two writes.
     - **The note.** It writes the item as Markdown with `notes.WriteNote`, through bm's CLI, as exercise `post` does.
       - Notes are titled by `key`, so a later mail about the same booking or order (rescheduled, shipped, cancelled) overwrites its note with the latest details.
       - Each note gets a `source` line with the account, the message's Gmail link and the subject. Its evidence is `(observed, email, <date>)`.
       - Person items are written only when no note in `people/` already lists that address as a `contact`. This is a disk read in `memory/store/notes`.
     - **The rows.** For each task and event, it calls `PutItem` with the note's permalink.
       - Extraction reports the state the mail shows: a cancelled booking, a paid bill, a delivered or cancelled order. `save` sets the row's `state` from it.
       - The note gets no status.
     
     Then it calls `FinishMessage(done)`. Messages that only skip are marked `skipped`. Retries are safe, because notes overwrite by title and rows upsert by key.
  - An on-failure task marks the batch `failed`, so it doesn't block later runs.
- **`inbox-digest`** (cron at 08:00 in `ROAMIND_TZ`, through `LocalCrons` as learning exercises use; `InboxHours`):
  - It runs for every Slack user with memory, because reminders also come from chat.
  - Users whose `Store.Channel(…, store.KindInbox, "")` is empty get a `user-setup` run, as `news-shorts` does for news.
  - **Pick (harness, no LLM):** `DueItems(user, today)` and the user's `failed` mail accounts. Nothing is posted when both are empty, so most mornings cost nothing.
  - **Write (one agent turn per user, `agent/prompts/inbox_digest.md`):**
    - The input is the picked rows: title, kind, event kind, date, note permalink and whether the item is overdue. It also lists the accounts that need reconnecting.
    - The turn reads each item's note, and its related notes (`build_context`), for what the digest needs: the checklist for tomorrow's flight with the user's travel preferences, the clinic's address, who the meeting is with, the amount of a bill, the goal a task serves.
    - It returns the digest text. The text is grouped as today, tomorrow and overdue, and it puts the overdue items first.
    - The notes hold text that came from third-party mail. So the turn gets a new tool set, `ToolsRecall`: bm's read tools only (`read_note`, `build_context`, `search_notes`), with no web, files, Bash or write tools, in a throwaway directory with no saved session.
    - Its output goes only to the user's own inbox channel.
  - **Post:** it reserves the slot, runs the turn, posts, then completes the slot. `inbox_digests` records the posted message's `channel_id` and `ts`, as `digests` does.
- **`user-setup`** gets an `inbox-channel` task (parent `bm-setup`), next to `news-channel`.
  - `SyncInbox` calls `openChannel(ctx, u, store.Channel{Kind: store.KindInbox}, InboxChannelName(u.DisplayName), intro)` (`worker/workflows/channels.go`). That one call creates the channel (with a `-n` suffix if the name is taken), records the row first, invites the user and posts the intro.
  - `InboxChannelName` is `channelName("inbox", userName, 40)`.
  - `chat-turn`'s `setUp` also starts user-setup when the inbox channel is missing, as it does for news.
- **Chat and items:** `chat-turn` gets the `items` tool set (`TurnTools` in `turn_tools.go`) in every channel, so "remind me to…" works anywhere.
- **Chat in the inbox channel:** a reply in a digest's thread is a chat turn that is given the digest text, as news-digest replies are. The digest text carries each item's key.
  - A reply such as "paid", "done" or "cancelled" closes the row with `close_item`. The note doesn't change.
  - "Moved to Tuesday" re-saves the row with `save_item` and edits the note's date.
- **Tasks in goal channels:** a goal's tasks are read through the items tools, with `list_items(folder: tasks/<goal>/)`. The goal paragraph of `system.md` points there for task state, next to `build_context` for the goal. Marking a task done is `close_item` in every channel.
- **Routing needs no new lookup:** `route` in `chat_turn.go` already reads the channel's kind with one `ChannelOf` call.
  - The change is that `route` returns that `*store.Channel` instead of its current `news bool`, so `Process` can set the goal, news or inbox part of the turn from the kind.
  - The owner's plain messages are then inbox turns, and an `{{if .Inbox}}` block is added to `agent/prompts/system.md`.
  - The skip reason for a mention changes from "own goal or news channel" to "own channel".
- Wiring goes in `workflows.go` `Register` (only with Memory), in the package doc list, and in `Deps.Mail` (the credentials dir, `data/secrets/mail`).

### 5. Roamind MCP server: `items` tool set (`mcp/tools/items.go`)
This is a new tool set next to `interests` and `jobs`, named `protocol.ServerItems`. It is pinned to the turn's user, as the others are.
- **`save_item(key, kind, event_kind?, note, title, date?, remind_on?, end?)`:** adds or reschedules a row through `PutItem`. The tool fills in `show_from` and `until` from `InboxLeads`, so the model gives only the dates.
  - The agent first writes or edits the note, then saves the row with the note's permalink.
  - Every new task or event note gets a row, dated or not.
  - `InboxLeads` moves to `memory/store`, so the worker and the server share it.
- **`close_item(key, state)`:** `state` is `done`, `cancelled` or `open` (to reopen an item).
- **`list_items(state?, from?, to?, folder?, key?)`:** with no arguments, it returns the open items. The arguments cover questions such as "what's on this week?", "which tasks are left on this goal?" and "did that order arrive?".
- `kind` and `state` are checked with `oneOf`. Dates must parse as ISO dates or date-times.

### 6. Basic Memory schemas (`memory/basic-memory-schemas/`)
The bodies are prose only: no `- ` lines, no `[[ ]]`, no "e.g.", and values containing `: ` are quoted. The existing schema tests enforce this.

**Built in PR #8**, ahead of the rest of the plan, so items shared in chat use it already. Task keeps `status` until the `items` table is built, and the rest of this section lands with it.

**No new schemas.** The existing types were reviewed against what mail produces:
- **A bill is a Task.** It is something to do by a date. "Paid" is closing its row. Its amount goes in `amount`, and the payee goes in the title.
- **An order is an Event with `kind: delivery`.** Once status is gone, an order is a dated arrival with a merchant, an order number (`confirmation`) and a tracking `link`.
- **A checklist is a field of its Event.** Each checklist serves one event.
  - The plan's reusable templates ("what I always pack for a flight") are Preferences (`subject: travel`). The digest turn reads them alongside the event.
  - Ticking items off had no reader, so it was dropped.
- **A provider is a Place or part of the title.** Clinics, hotels and venues are Places linked with `at`, as workplaces already are. An airline or merchant name goes in the title.

Without Bill and Order types, "what did I pay this year" is a search over tasks with an `amount`, and "my orders" is a search over events with `kind: delivery`.

**There is no separate Reminder type.** A reminder is a row in `items`. It comes either from something to do, which is a Task note with a row, or from a dated thing, which is an Event note with a row whose lead time comes from its kind.

**Task and Event notes have no status.** The note says what the thing is, and its `items` row says whether it is open, done or cancelled.
- Both schema bodies say that the note is tracked with the `items` tools: `save_item` when it is created or rescheduled, and `close_item` when it is done, paid, delivered or cancelled. This keeps the rule with the types.
- Event never had a status. Goal keeps its `status`, since goals aren't items and goal channels read it (`notes.Goals`).

**Past items stay in memory as history.** In chat, the model never loads them wholesale. It looks them up on demand with `search_notes` or `build_context`, for questions such as "when did I last see the dentist?", "what was my booking reference?" or "is that order still returnable?". Year folders keep each folder small.
- **Task.md (changed)**:
  - `task_of` becomes optional. Tasks linked to a goal stay in `tasks/<goal>/`, and tasks with no fitting goal (most tasks from mail, and "remind me to…" from chat) go in `tasks/`.
  - Removes `status`.
  - Adds `amount?: string, the money involved, with its currency` (a bill to pay, a refund to claim, a deposit) and `source?: string, the mail it came from`.
  - There is no `remind_on` field, because the reminder lives in the item row.
  - The text "ask which goal" is replaced with "link a goal when one fits", and "Update its status as it moves along" with the `items` rule. `docs/architecture.md` is updated to match.
  - Existing task notes are left as they are, and their `[status]` lines just become plain observations. No backfill is done while bootstrapping.
- **Event.md (changed)**:
  - Adds these fields:
    - `kind?(enum): [appointment, travel, stay, ticket, meeting, delivery, renewal, milestone, other]`;
    - `end?: string, ISO date or date-time when it ends`;
    - `confirmation?: string, booking reference, order number or ticket number`;
    - `link?(array): string, related URLs such as tracking or the booking page`, as Goal has;
    - `checklist?(array): string, one thing to bring or do beforehand per line`;
    - `source?`.
  - Saved in `events/<YYYY>/`.
  - Appointments and travel bookings link to a Place with `at` and to a Person with `with`.
  - A shipping mail updates the delivery event's date and `link`.
  - The body says that a checklist starts from the user's preferences for that kind of event, such as their travel preferences.
  - Extraction proposes a checklist for travel, stays and appointments ("bring insurance card, referral letter").
- Person, Place, Preference: unchanged. An email address goes in `contact`, and a reusable packing list is a Preference.

### 7. Prompt and agent code
- `agent/mail.go`: `MailBatch` input, `ExtractMailPrompt`, `ExtractedMailSchema` (JSON schema, descriptions carry the field formats), and Go types, following `agent/jobs.go`.
- `agent/prompts/extract_mail.md`: positive instructions.
  - Give the data (user, date, goals, messages).
  - Say what to capture and how to date it: absolute ISO dates in the user's timezone.
  - Ask for one item per real-world thing, keyed by its reference number. Treat JSON-LD as the most precise source.
  - Mark promotional, automated-notification and security-code mail as skip.
- `agent/inbox.go` + `agent/prompts/inbox_digest.md`:
  - The input is the due rows and the accounts to reconnect, plus the user's name, timezone and today's date.
  - The prompt says to read each item's note and related notes for what the user needs today, and to write a short digest grouped as overdue, today and tomorrow. Each item is given with its key.
- `agent/protocol`: `ToolsRecall` (bm read tools only, no saved session) and `ServerItems`. `agent/mcp_servers.go` allows only bm's read tools for `ToolsRecall`.
- `agent/prompts/system.md`:
  - One line saying that tasks and events are tracked with the `items` tools. Their state lives there, not in the note.
  - The goal paragraph reads the goal's tasks with `list_items`.
  - The `{{if .Inbox}}` block.
- `extract_mail.md` and `ExtractedMailSchema`:
  - Each item carries a `state` (`open`, `done` or `cancelled`), taken from what the mail shows.
  - The prompt says how mail maps to the two types, as in the table in section 2: a bill to pay is a task with `remind_on` three days before `due`, an order is a delivery event, and an automatic renewal is a renewal event.

### 8. Docs, config
- `docs/architecture.md`: new "Email assistant" section with a flow diagram (mail → notes + items → digest). The Memory section's required fields (a task no longer requires `status`) and Task rule are updated, along with a note that `items` owns item state. The schema list is unchanged.
- `docs/deployment.md`: GCP setup, oauth2l, `task mail-connect`.
- `README.md`: feature line, plus a design principle: "Used Google's oauth2l to connect mail accounts offline…" (per README voice).
- `.env.example`: `ROAMIND_MAIL_DIR` (default `data/secrets/mail`).
- Privacy note in the docs: mail text is sent to Claude for extraction, and only extracted items are stored. The raw mail is not stored.

## Considered: Gmail push through Pub/Sub
Gmail can push new-mail notifications through Pub/Sub to a webhook, which could trigger a Hatchet workflow. The plan keeps the 15-minute poll instead:
- **A push carries no mail.** It is `{emailAddress, historyId}`, base64-encoded, so the triggered run still calls `history.list` and `messages.get`, as `fetch` does.
- **It removes no parts:**
  - `users.watch` expires after 7 days, so a cron is still needed to renew it.
  - Google recommends a periodic history sync anyway, because notifications can be delayed or dropped.
- **It adds setup:** a Pub/Sub topic, a publish grant for `gmail-api-push@system.gserviceaccount.com`, and a push subscription.
- **The endpoint couldn't be authenticated.** Pub/Sub push signs each request with a new OIDC token. Hatchet's inbound webhooks check basic auth, a fixed API key or HMAC, and none of those can verify the token. Hatchet also has no Google or Pub/Sub source.
- **The gain is invisible.** Mail would be handled within seconds instead of within 15 minutes, but the output is an 08:00 digest.

Push is worth adding if Roamind sends instant alerts, such as a gate change or a cancelled appointment within the hour. It would be a small change:
- A Hatchet webhook (event key `gmail`) starts the same `mail-account` run, for the address in the payload.
- `mail-sync` stays, as the watch-renewal and fallback cron.

## Files
New:
- `memory/sql-schemas/{09_mail.sql,10_items.sql}`
- `memory/store/{mail.go,items.go}` and their tests
- `connectors/mail/{gmail.go,message.go,*_test.go,testdata/}`
- `agent/{mail.go,inbox.go}`, `agent/prompts/{extract_mail.md,inbox_digest.md}`
- `mcp/tools/items.go` and its test
- `worker/workflows/{mail_sync.go,mail_account.go,inbox_digest.go}` and their tests, with `testdata/{extract_mail.jsonl,inbox_digest.jsonl}`

Changed:
- `Task.md`, `Event.md`
- `worker/workflows/{workflows.go,user_setup.go,chat_turn.go,turn_tools.go}`
- `memory/store/channels.go` (`KindInbox`) and the `kind` comment in `memory/sql-schemas/08_channels.sql`
- `mcp/tools/tools.go` (the `items` set), `agent/protocol/turn.go`, `agent/mcp_servers.go`
- `worker/cmd/worker/main.go`
- `agent/prompts/system.md`
- `Taskfile.yml`, `docs/*`, `README.md`, `.env.example`
- `connectors/go.mod`, `worker/go.mod`: add the gmail/oauth2 dependencies, edited by hand in `worker/` (no `GOWORK=off go mod tidy`).

Reused:
- `notes.WriteNote` and `notes.Goals` (`memory/store/notes/`)
- the reserve/complete pattern of `digests` (`memory/store/news.go`)
- `openChannel`, `channelName` and the `channels` store API (`Channel`, `Channels`, `ChannelOf`) for the inbox channel (`worker/workflows/channels.go`, `memory/store/channels.go`)
- `LocalCrons`, and the skip pattern of the `learning-exercise` DAG
- `ToolsNone` structured turns (`agent/jobs.go`, `worker/workflows/job_application.go`)
- the tool-set pattern of `mcp/tools` (`interests`, `jobs`) and `TurnTools`
- `storetest` for the store tests

All work goes in one branch and one PR.

## Verification
1. **Unit tests:** `go test ./...` in `memory`, `connectors`, `agent`, `mcp` and `worker`. This covers:
   - the schema tests (no fact lines or links, the YAML parses);
   - store CRUD on the real SQL files, including `DueItems` with the lead times and expiry of tasks, deliveries and other events, overdue tasks, and closed or expired rows;
   - Gmail message parsing from testdata (multipart, HTML-only, JSON-LD, history 404 → full sync);
   - `mail-account` with a recorded `extract_mail.jsonl` turn writing the expected notes and rows. A second mail with the same key reschedules the row, and a cancellation closes it;
   - the `items` tools (`save_item` computes `show_from`/`until`, and `close_item` closes the row);
   - `ToolsRecall` turns get bm's read tools only;
   - `inbox-digest` posting nothing when no rows are due, and posting a recorded `inbox_digest.jsonl` turn when rows are due.
2. **Build and init:** `task build && task init`, then check the tables in `data/roamind.db` and the updated Task and Event schema notes in a user's project.
3. **Live run:**
   - Run `task mail-connect -- sainageswar@gmail.com` for two accounts.
   - Trigger `mail-sync` with the Hatchet CLI (`hatchet` skill: trigger-and-watch).
   - Check `mail_messages` statuses, the `items` rows, and the new notes under `data/memory/users/<p>/notes/{tasks,events,people}`. A bill should be a task with `amount`, and an order a delivery event with its tracking link.
   - Run bm `schema_validate` on the notes.
4. **Digest:**
   - Trigger `inbox-digest` by hand. The `#inbox-sai` channel should be created, with a `channels` row of `kind = 'inbox'`. The digest is posted, with each event's checklist and the matching travel preferences pulled from memory.
   - Reply "done" to an item. The row's `state` should change, and the item shouldn't appear the next day.
   - In any channel, say "remind me to call the bank on Friday". A task note and a row should be created, and the item shows in Friday's digest.
   - In a goal channel, add an undated task and then mark it done. Its row should go from open to done, and `list_items` for the goal's folder should show that.
5. **Failure path:** revoke an account's access in its Google account settings. The next sync should mark it `failed`, and the digest should say it needs reconnecting.
