# Email assistant (plan, not built)

Status: drafted 2026-10-21, to revisit. The `channels` table it relies on is live (PR #6, deployed 2026-10-21): the inbox channel is one more kind in it, so this plan adds no channel table and no migration.

## Context
Roamind should read the user's Gmail (several accounts per user) and turn mail into structured Basic Memory notes: tasks, reminders, events/appointments, bills and renewals, travel bookings, orders and deliveries, people, and checklists of things to carry. A morning digest in a private `#inbox-<name>` Slack channel then shows what is due. This also delivers the roadmap's pending "reminders" item. Reminders are not a type of their own: they come from dates on tasks, events, bills and orders, including tasks saved from chat.

Decided with the user: connecting accounts is **offline and manual for now** (an operator task); new mail is **polled by a Hatchet cron**; due items go to a **morning digest in `#inbox-<name>`**; capture **bills & renewals, travel & bookings, orders & deliveries, people & contacts, appointments**, plus a **Checklist** schema.

Research findings that shape the design:
- **Scope and app status.** `gmail.readonly` is a *restricted* scope. A personal, unverified app is allowed up to 100 users behind a click-through warning. The OAuth consent screen must be **"In production"**: in "Testing" mode, refresh tokens expire after 7 days. Each client/user pair has a cap on refresh tokens (about 50–100), so reconnecting many times silently revokes the oldest token.
- **Sync model** (Gmail sync guide). Do a full sync with `messages.list` and store the latest `historyId`. After that, call `history.list` from the stored id. A 404 means the history has expired (it is kept for about a week or more), so fall back to a full sync. Pub/Sub only replaces the trigger, and polling is enough here.
- **schema.org markup.** Many senders embed schema.org JSON-LD in their mail (Gmail markup): FlightReservation, LodgingReservation, EventReservation, ParcelDelivery, Order, Invoice. Pass it to extraction as-is; it is far more reliable than prose.
- **Prior art.** Inbox Zero (AGPL) does triage, not extraction into a personal knowledge base. Nothing to reuse.

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

### 2. Data: `memory/sql-schemas/09_mail.sql` + `memory/store/mail.go` (+ `mail_test.go`)
- `mail_accounts(address PK, user_id, history_id, status active|failed, error, connected_at, synced_at)`
- `mail_messages(address, message_id, user_id, status picked|done|skipped|failed, received_at, done_at, PK(address, message_id))`. Every message seen is recorded before it is read, so none is extracted twice. This follows the `job_postings` pattern.
- **The inbox channel is a row of the existing `channels` table** (`memory/sql-schemas/08_channels.sql`, `memory/store/channels.go`), with `kind = 'inbox'` and `ref = ''`. Each user has one inbox channel, and the empty `ref` in the primary key `(user_id, kind, ref)` enforces that, as for news.
  - Add `store.KindInbox = "inbox"` next to `KindGoal` and `KindNews`, and add `inbox` to the `kind` comment in `08_channels.sql`. `kind` is free text, so the table doesn't change and needs no migration.
  - Lookups use the existing API: `Store.Channel(ctx, userID, store.KindInbox, "")` for a user's channel, and `Store.Channels(ctx, store.KindInbox)` for every user's.
  - An inbox channel can't also be a user's goal or news channel, because of `UNIQUE (channel_id, user_id)`.
- `inbox_digests(user_id, day, channel_id, ts, posted_at, UNIQUE(user_id, day))`. The slot is reserved, then the message posted, then the slot completed, as `digests` does.
- Store functions keep the repo's short names: `MailAccounts`, `SetHistory`, `FailAccount`, `PickMessages`, `PendingMessages`, `FinishMessage`, `ReserveInbox`, `CompleteInbox`.

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
  2. **`extract`**: one `ToolsNone` turn over the batch (`agent/prompts/extract_mail.md`), with structured output. The mail is third-party text, so the turn gets no tools and no memory, as news ranking and job tailoring do. Input: the user's name, email, timezone and today's date; their active goals (title and permalink, from `notes.Goals`) so tasks can be linked; and each message's headers, text and JSON-LD. Output: per message, `skip` with a reason (newsletter, notification, OTP or security code) or a list of items. Each item has a `kind` (task, event, bill, order, person, checklist) and fields matching that schema. Each item also carries a `key`, a stable title: a confirmation, order or invoice number when there is one. Retried once.
  3. **`save`**: the harness writes each item as Markdown with `notes.WriteNote`, through bm's CLI, as exercise `post` does. Notes are titled by `key`, so a later mail about the same booking or order (rescheduled, shipped, cancelled) overwrites its note with the latest state. Each note gets a `source` line with the account, the message's Gmail link and the subject, and its evidence is `(observed, email, <date>)`. Person items are written only when no note in `people/` already lists that address as a `contact`; this is a disk read in `memory/store/notes`. No extra reminder notes are written: the dates on the items themselves drive the digest. Then `FinishMessage(done)`. Messages that only skip are marked `skipped`. Retries are safe because writes overwrite by title.
  - An on-failure task marks the batch `failed`, so it doesn't block later runs.
- **`inbox-digest`** (cron at 08:00 in `ROAMIND_TZ`, through `LocalCrons` as learning exercises use; `InboxHours`):
  - It runs for every Slack user with memory, because reminders also come from chat.
  - Users whose `Store.Channel(…, store.KindInbox, "")` is empty get a `user-setup` run, as `news-shorts` does for news. The digest is posted to that channel, and `inbox_digests` records the posted message's `channel_id` and `ts`, as `digests` does.
  - It is harness code, with no LLM. A new `notes.Agenda(project, day)` reads only the dated lines (`due`, `remind_on`, `date`, `expected`, `return_by`) and statuses of the notes in `tasks/`, `events/<this year and next>/`, `bills/<…>/` and `orders/<…>/`. Past items drop out by date, so history never grows the digest. The year folders bound the scan, so its cost doesn't grow with years of history. The lead times are per kind, set in the harness (`InboxLeads`). The digest lists:
    - open tasks whose `remind_on` or `due` is today or overdue, or whose `due` is tomorrow;
    - events (appointments, flights, stays, tickets) that are not cancelled and fall today or tomorrow, each with its checklist;
    - unpaid bills due within 3 days;
    - orders arriving today, and return windows closing within 3 days;
    - accounts that need reconnecting.
  - It reserves the slot, posts, then completes the slot. Nothing is posted when nothing is due.
- **`user-setup`** gets an `inbox-channel` task (parent `bm-setup`), next to `news-channel`. `SyncInbox` calls `openChannel(ctx, u, store.Channel{Kind: store.KindInbox}, InboxChannelName(u.DisplayName), intro)` (`worker/workflows/channels.go`). That one call creates the channel with a `-n` suffix if the name is taken, records the row first, invites the user and posts the intro. `InboxChannelName` is `channelName("inbox", userName, 40)`. `chat-turn`'s `setUp` also starts user-setup when the inbox channel is missing, as it does for news.
- **Chat in the inbox channel**: a reply in a digest's thread is a chat turn that is given the digest text, as news-digest replies are. The agent marks items done (task `done`, bill `paid`, event `cancelled`) with `edit_note`. Routing needs no new lookup: `route` in `chat_turn.go` already reads the channel's kind with one `ChannelOf` call. The change is that `route` returns that `*store.Channel` instead of its current `news bool`, so `Process` can set the goal, news or inbox part of the turn from the kind. The owner's plain messages are then inbox turns, and an `{{if .Inbox}}` block is added to `agent/prompts/system.md`. The skip reason for a mention changes from "own goal or news channel" to "own channel".
- Wiring goes in `workflows.go` `Register` (only with Memory), in the package doc list, and in `Deps.Mail` (the credentials dir, `data/secrets/mail`).

### 5. Basic Memory schemas (`memory/basic-memory-schemas/`)
The bodies are prose only: no `- ` lines, no `[[ ]]`, no "e.g.", and values containing `: ` are quoted. The existing schema tests enforce this. Each email-derived note can carry `source?: string, the mail it came from`.

**There is no separate Reminder type.** A reminder is either something to do, which makes it a Task with `remind_on`, or a nudge about a dated thing, which comes from that thing's own date and the digest's lead time. A separate note would duplicate the date and drift from it when the mail changes.

**Status exists only where the date can't tell.** Nothing has an active/closed status: an event in the past is over. Status records what happened outside time: done or cancelled for a task, paid for a bill, the shipping state of an order, a cancelled event.

**Past items stay in memory as history.** In chat, the model never loads them wholesale. It looks them up on demand with `search_notes` or `build_context`, for questions such as "when did I last see the dentist?", "what was my booking reference?" or "is that order still returnable?". Year folders keep each folder small.
- **Task.md (changed)**:
  - `task_of` becomes optional. Tasks linked to a goal stay in `tasks/<goal>/`, and tasks with no fitting goal (most tasks from mail, and "remind me to…" from chat) go in `tasks/`.
  - Adds `remind_on?` (ISO date or date-time to bring it up, when earlier than or without `due`) and `source?`.
  - The text "ask which goal" is replaced with "link a goal when one fits". `docs/architecture.md` is updated to match.
- **Event.md (changed)**:
  - Adds `kind?(enum): [appointment, flight, train, stay, ticket, reservation, meeting, trip, milestone, other]`, `end?`, `provider?` (airline, clinic, venue), `confirmation?`, `status?(enum): [confirmed, cancelled]` (absent means confirmed) and `source?`.
  - Saved in `events/<YYYY>/`.
  - Appointments and travel bookings are Events, linked to Place with `at` and to Person with `with`.
- **Bill.md (new)**:
  - Fields: `payee`, `amount` (with its currency), `due`, `status(enum): [due, paid, autopay]`, `kind(enum): [bill, invoice, subscription, renewal]`, `recurring?` and `source?`.
  - Saved in `bills/<YYYY>/`.
- **Order.md (new)**:
  - Fields: `merchant`, `order_number?`, `status(enum): [ordered, shipped, delivered, returned, cancelled]`, `expected?`, `tracking?` (URL), `return_by?` and `source?`.
  - Saved in `orders/<YYYY>/`.
- **Checklist.md (new)**: `kind(enum): [packing, documents, preparation, shopping, other]`, `checklist_for?: the Event, Task or Goal it serves`, `item(array)` (one thing to carry or do per line) and `done?(array)` (items ticked off, moved from item). A checklist without `checklist_for` is the user's reusable template of its kind, which a new event's checklist starts from. Saved in folder `checklists/`. Extraction proposes one for flights, stays and appointments ("bring insurance card, referral letter").
- Person, Place: unchanged; an email address goes in `contact`.
- `notes.Setup` folders and `task init` copy the new schema files automatically, since they come from the directory. Update the schema list in docs.

### 6. Prompt and agent code
- `agent/mail.go`: `MailBatch` input, `ExtractMailPrompt`, `ExtractedMailSchema` (JSON schema, descriptions carry the field formats), and Go types, following `agent/jobs.go`.
- `agent/prompts/extract_mail.md`: positive instructions. Give the data (user, date, goals, messages). Say what to capture and how to date it: absolute ISO dates in the user's timezone. Ask for one item per real-world thing, keyed by its reference number. Treat JSON-LD as the most precise source. Mark promotional, automated-notification and security-code mail as skip.

### 7. Docs, config
- `docs/architecture.md`: new "Email assistant" section with a flow diagram; the Memory section's schema list and Task rule updated.
- `docs/deployment.md`: GCP setup, oauth2l, `task mail-connect`.
- `README.md`: feature line, plus a design principle: "Used Google's oauth2l to connect mail accounts offline…" (per README voice).
- `.env.example`: `ROAMIND_MAIL_DIR` (default `data/secrets/mail`).
- Privacy note in the docs: mail text is sent to Claude for extraction, and only extracted items are stored. The raw mail is not stored.

## Files
New:
- `memory/sql-schemas/09_mail.sql`
- `memory/store/mail.go` and its test
- `connectors/mail/{gmail.go,message.go,*_test.go,testdata/}`
- `agent/mail.go`, `agent/prompts/extract_mail.md`
- `worker/workflows/{mail_sync.go,mail_account.go,inbox_digest.go}` and their tests, with `testdata/extract_mail.jsonl`
- `memory/store/notes/agenda.go` and its test
- `memory/basic-memory-schemas/{Bill,Order,Checklist}.md`

Changed:
- `Task.md`, `Event.md`
- `worker/workflows/{workflows.go,user_setup.go,chat_turn.go}`
- `memory/store/channels.go` (`KindInbox`) and the `kind` comment in `memory/sql-schemas/08_channels.sql`
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
- `storetest` for the store tests

All work goes in one branch and one PR.

## Verification
1. **Unit tests:** `go test ./...` in `memory`, `connectors`, `agent` and `worker`. This covers:
   - the schema tests (no fact lines or links, the YAML parses);
   - store CRUD on the real SQL files;
   - Gmail message parsing from testdata (multipart, HTML-only, JSON-LD, history 404 → full sync);
   - `mail-account` with a recorded `extract_mail.jsonl` turn writing the expected notes;
   - `Agenda` picking due items;
   - the digest text.
2. **Build and init:** `task build && task init`, then check the tables in `data/roamind.db` and the new schema notes in a user's project.
3. **Live run:**
   - Run `task mail-connect -- sainageswar@gmail.com` for two accounts.
   - Trigger `mail-sync` with the Hatchet CLI (`hatchet` skill: trigger-and-watch).
   - Check `mail_messages` statuses and the new notes under `data/memory/users/<p>/notes/{tasks,events,bills,orders,checklists,people}`.
   - Run bm `schema_validate` on them.
4. **Digest:** trigger `inbox-digest` by hand. The `#inbox-sai` channel should be created, with a `channels` row of `kind = 'inbox'`, and the digest posted. Reply "done" to an item and check that the note's status changes.
5. **Failure path:** revoke an account's access in its Google account settings. The next sync should mark it `failed`, and the digest should say it needs reconnecting.
