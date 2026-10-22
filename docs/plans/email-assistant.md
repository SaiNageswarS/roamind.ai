# Email assistant (plan; mail still to build)

Status: drafted 2026-10-21, revised 2026-10-22, to revisit. What is left is reading mail. Everything mail feeds into is built and described in `docs/architecture.md`:
- the Task and Event schemas (PR #8);
- the `items` table and the `items` tools (PRs #10 and #11);
- the inbox channel and the morning `inbox-digest` (2026-10-22).

## Context
Roamind should read the user's Gmail (several accounts per user) and turn mail into structured Basic Memory notes, each task and event with its row in `items`: bills, travel bookings, orders and deliveries, appointments, commitments, people, and checklists of things to carry. The morning inbox digest then shows them when they are due, as it already does for items saved in chat.

Decided with the user:
- Connecting accounts is **offline and manual for now** (an operator task).
- New mail is **polled by a Hatchet cron**.
- Mail is captured for **bills, travel & bookings, orders & deliveries, people & contacts, appointments**, plus checklists.
- **No new schemas**:
  - a bill is a Task;
  - a booking or delivery is an Event;
  - a checklist is a field of its Event;
  - a reusable packing list is a Preference.

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

### 2. Data: `memory/sql-schemas/11_mail.sql` + `memory/store/mail.go` (+ `mail_test.go`)
- `mail_accounts(address PK, user_id, history_id, status active|failed, error, connected_at, synced_at)`
- `mail_messages(address, message_id, user_id, status picked|done|skipped|failed, received_at, done_at, PK(address, message_id))`. Every message seen is recorded before it is read, so none is extracted twice. This follows the `job_postings` pattern.
- Store functions keep the repo's short names: `MailAccounts`, `SetHistory`, `FailAccount`, `PickMessages`, `PendingMessages`, `FinishMessage`.

**What mail turns into:**

| Mail | Note | Item row |
|---|---|---|
| A bill or invoice to pay | Task "Pay \<payee> \<invoice number>", with `due` and `amount` | `remind_on` is `due` minus 3 days, set by extraction; the row closes as done when paid |
| An order | Event, `kind: delivery`, dated by the expected delivery, with the tracking `link` | closes as done when delivered, or as cancelled |
| A flight, train, stay, ticket or appointment | Event with its `kind`, `confirmation` and `checklist` | closes as cancelled when cancelled |
| A commitment ("I'll send it by Friday") | Task | |

- **Automatic renewals aren't captured for now.** Event has no `renewal` kind; a renewal that needs action, such as cancelling a subscription in time, is a task.
- **Return windows get no reminder.** Most orders aren't returned, so one reminder per order would be mostly noise. The delivery event's body records the window ("returnable until 2026-11-20"). When the user says they want to return something, the agent creates a task due that day.
- Rows from mail have `source` `mail:<address>/<message id>`.

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
       - Each item has a `kind` (task, event, person), the fields of that schema, a `key` and a `state` (`open`, `done` or `cancelled`, from what the mail shows).
       - The key is a stable title: a confirmation, order or invoice number when there is one.
       - Tasks can carry a `remind_on`. Bills always do.
       - Events carry their `checklist` lines.
     - Retried once.
  3. **`save`**: for each item, the harness does two writes.
     - **The note.** It writes the item as Markdown with `notes.WriteNote`, through bm's CLI, as exercise `post` does.
       - Notes are titled by `key`, so a later mail about the same booking or order (rescheduled, shipped, cancelled) overwrites its note with the latest details.
       - Task and Event have no `source` field. The note's opening text names the account, the message's Gmail link and the subject, and the row's `source` column records the message id. Its evidence is `(observed, email, <date>)`.
       - Person items are written only when no note in `people/` already lists that address as a `contact`. This is a disk read in `memory/store/notes`.
     - **The rows.** For each task and event, it calls `PutItem` with the note's permalink, its dates through `store.InboxLeads`, and the `state` extraction reports: a cancelled booking, a paid bill, a delivered or cancelled order.

     Then it calls `FinishMessage(done)`. Messages that only skip are marked `skipped`. Retries are safe, because notes overwrite by title and rows upsert by key.
  - An on-failure task marks the batch `failed`, so it doesn't block later runs.
- **`inbox-digest`** changes:
  - `check-skip` also picks the user's `failed` mail accounts, and the day is not skipped while one needs reconnecting.
  - The draft prompt lists the accounts to reconnect.
  - **Revisit the draft turn's tools.** The draft turn now runs like exercise drafting: workspace tools, memory, a saved session that the digest's thread resumes, so a reply closes items with the items tools. Once notes hold text taken from third-party mail, consider a read-only `ToolsRecall` turn (bm's `read_note`, `build_context` and `search_notes` only, no saved session). The thread would then need the digest text, from `inbox_digests.text`, given to its chat turns, as news-digest replies are.
- Wiring goes in `workflows.go` `Register` (only with Memory), in the package doc list, and in `Deps.Mail` (the credentials dir, `data/secrets/mail`).

### 5. Prompt and agent code
- `agent/mail.go`: `MailBatch` input, `ExtractMailPrompt`, `ExtractedMailSchema` (JSON schema, descriptions carry the field formats), and Go types, following `agent/jobs.go`.
- `agent/prompts/extract_mail.md`: positive instructions.
  - Give the data (user, date, goals, messages).
  - Say what to capture and how to date it: absolute ISO dates in the user's timezone.
  - Ask for one item per real-world thing, keyed by its reference number. Treat JSON-LD as the most precise source.
  - Say how mail maps to the two types, as in the table in section 2: a bill to pay is a task with `remind_on` three days before `due`, and an order is a delivery event.
  - Mark promotional, automated-notification and security-code mail as skip.

### 6. Docs, config
- `docs/architecture.md`: new "Email assistant" section with a flow diagram (mail → notes + items → inbox digest).
- `docs/deployment.md`: GCP setup, oauth2l, `task mail-connect`.
- `README.md`: the inbox digest's feature line names mail as a source, plus a design principle: "Used Google's oauth2l to connect mail accounts offline…" (per README voice).
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
- `memory/sql-schemas/11_mail.sql`, `memory/store/mail.go` and its test
- `connectors/mail/{gmail.go,message.go,*_test.go,testdata/}`
- `agent/mail.go`, `agent/prompts/extract_mail.md`
- `worker/workflows/{mail_sync.go,mail_account.go}` and their tests, with `testdata/extract_mail.jsonl`

Changed:
- `worker/workflows/{workflows.go,inbox_digest.go}`, `agent/inbox.go`, `agent/prompts/inbox_digest.md` (accounts to reconnect)
- `worker/cmd/worker/main.go`
- `Taskfile.yml`, `docs/*`, `README.md`, `.env.example`
- `connectors/go.mod`, `worker/go.mod`: add the gmail/oauth2 dependencies, edited by hand in `worker/` (no `GOWORK=off go mod tidy`).

Reused:
- `notes.WriteNote` and `notes.Goals` (`memory/store/notes/`)
- `PutItem` and `InboxLeads` (`memory/store/items.go`)
- `ToolsNone` structured turns (`agent/jobs.go`, `worker/workflows/job_application.go`)
- the dispatcher pattern of `news-shorts`
- `storetest` for the store tests

All work goes in one branch and one PR.

## Verification
1. **Unit tests:** `go test ./...` in `memory`, `connectors`, `agent` and `worker`. This covers:
   - store CRUD for `mail_accounts` and `mail_messages` on the real SQL files;
   - Gmail message parsing from testdata (multipart, HTML-only, JSON-LD, history 404 → full sync);
   - `mail-account` with a recorded `extract_mail.jsonl` turn writing the expected notes and rows. A second mail with the same key reschedules the row, and a cancellation closes it;
   - `inbox-digest` listing a failed account, and not skipping the day for it.
2. **Build and init:** `task build && task init`, then check the mail tables in `data/roamind.db`.
3. **Live run:**
   - Run `task mail-connect -- sainageswar@gmail.com` for two accounts.
   - Trigger `mail-sync` with the Hatchet CLI (`hatchet` skill: trigger-and-watch).
   - Check `mail_messages` statuses, the `items` rows, and the new notes under `data/memory/users/<p>/notes/{tasks,events,people}`. A bill should be a task with `amount`, and an order a delivery event with its tracking link.
   - Run bm `schema_validate` on the notes.
   - The next morning's inbox digest should show the mail's items that are due.
4. **Failure path:** revoke an account's access in its Google account settings. The next sync should mark it `failed`, and the digest should say it needs reconnecting.
