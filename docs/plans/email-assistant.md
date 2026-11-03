# Email assistant (plan; built)

Status: drafted 2026-10-21, revised 2026-10-22, redesigned 2026-11-01 around Google Apps Script, built 2026-11-02. How it works now is in the Email assistant section of `docs/architecture.md`, and setting it up in `docs/deployment.md`. The build differs from this plan in that:
- `mail-message` runs are serialized across all accounts, not per user: the webhook's event names the account, so its concurrency key can't be the user.
- An update rewrites the note's file in place, found by permalink with `bm tool read-note`, because bm names a file after the title it was first written with; writing it under another title made a second note.
- The script reads and labels per message through the Gmail advanced service; `GmailApp` labels whole threads.
- Each account has its own key (`task mail-connect`), and the worker checks a second signature with it: with only the shared `ROAMIND_MAIL_SECRET`, one user's script could send mail as another's account.
- Mail that wasn't saved (a failed or stuck run) is listed in the inbox digest for a week; removing its `roamind/sent` label sends it again. A message the script fails on 8 runs in a row is labeled `roamind/error`.
- The turn reads a new note's folder and title before naming it, and updates a note already there; its content always replaces the note, written into the existing file when the permalink is taken.

Everything mail feeds into is built and described in `docs/architecture.md`:
- the Task and Event schemas (PR #8);
- the `items` table and the `items` tools (PRs #10 and #11);
- the inbox channel and the morning `inbox-digest` (2026-10-22).

## Context
Roamind should read the user's Gmail (several accounts per user) and turn mail into structured Basic Memory notes, each task and event with its row in `items`: bills, travel bookings, orders and deliveries, appointments, commitments, people, and checklists of things to carry. The morning inbox digest then shows them when they are due, as it already does for items saved in chat.

Decided with the user:
- **Gmail is read by a Google Apps Script** in each Gmail account, not by Roamind through the Gmail API. The script classifies mail broadly with a DeepSeek model on OpenRouter, labels it in Gmail, and sends what's worth keeping to a Hatchet webhook.
- **Roamind relates mail to memory.** A `claude -p` turn reads the user's notes to tie each mail to the booking, order, person or goal it's about, and decides what to save.
- Connecting an account is **offline and manual for now** (an operator task).
- Mail is captured for **bills, travel & bookings, orders & deliveries, people & contacts, appointments**, plus checklists.
- **No new schemas**:
  - a bill is a Task;
  - a booking or delivery is an Event;
  - a checklist is a field of its Event;
  - a reusable packing list is a Preference.

Findings that shape the design:
- **No Gmail OAuth app.** The script runs as the account's owner, authorized once in the Apps Script editor. That avoids what reading Gmail from Roamind needed: an OAuth client, the restricted `gmail.readonly` scope with its consent-screen rules (refresh tokens expire after 7 days in "Testing"; each client/user pair caps refresh tokens), `oauth2l`, and `historyId` sync with its fallback.
- **The script can reach Hatchet.** Apps Script runs on Google's servers, outside the tailnet. `task funnel` already makes the whole Hatchet webhooks prefix public (`/api/v1/stable/tenants/<tenant>/webhooks`), so a new `gmail` webhook under it needs no Tailscale change. Hatchet's generic webhooks check an HMAC of the raw request body (`api/v1/server/handlers/v1/webhooks/receive.go`, Hatchet v0.110.22), which Apps Script computes with `Utilities.computeHmacSha256Signature`.
- **Quotas are no concern.** A consumer Google account allows 90 minutes a day of trigger runtime and 20,000 `UrlFetchApp` calls a day. One run handles at most 20 messages, within the 6-minute run limit.
- **schema.org markup.** Many senders embed schema.org JSON-LD in their mail (Gmail markup): FlightReservation, LodgingReservation, EventReservation, ParcelDelivery, Order, Invoice. The script passes it on as-is; it is far more reliable than prose.
- **DeepSeek on OpenRouter.** DeepSeek's own API keeps data in China. OpenRouter serves DeepSeek's open models from other hosts too, and `provider: {data_collection: "deny"}` restricts a request to hosts that don't store or train on it.
- **Prior art.** Inbox Zero (AGPL) does triage, not extraction into a personal knowledge base. Nothing to reuse.

## Flow

```
Gmail ─▶ Apps Script (time trigger, every 15 min)
           search new mail ─▶ OpenRouter (DeepSeek): broad category
           label roamind/<category>
           POST message + category ─▶ Tailscale Funnel ─▶ Hatchet webhook gmail (event mail:received)
                                                           ─▶ worker: mail-message
                                                                check ─▶ relate (claude -p, reads memory) ─▶ save
                                                           ─▶ notes + items rows ─▶ inbox digest
```

## Design

### 1. Apps Script: `deploy/apps-script/` (`Code.js`, `appsscript.json`)
The source lives in the repo. The operator pushes it to each Gmail account with Google's `clasp` CLI (`task mail-script`), or pastes it into the Apps Script editor.

- **Script properties** (Project settings → Script properties):
  - `OPENROUTER_API_KEY`;
  - `OPENROUTER_MODEL`: a DeepSeek model id on OpenRouter;
  - `WEBHOOK_URL`: printed by `task webhook`;
  - `WEBHOOK_SECRET`: the `ROAMIND_MAIL_SECRET` of `.env`.
- **`setup()`**, run once by hand: creates the labels and installs a 15-minute time trigger for `run()`. Running it is also how the owner authorizes the script's Gmail, fetch and trigger scopes.
- **`run()`**:
  1. **Find.** `GmailApp.search('in:inbox -category:promotions -category:social -label:roamind/sent -label:roamind/skip newer_than:3d')`, and the same over `in:sent`, because the user's own replies hold commitments ("I'll send it by Friday"). It takes the oldest 20 messages; leftovers wait for the next run.
  2. **Read.** For each message:
     - headers (`from`, `to`, `cc`, `date`, `subject`) and the Gmail link;
     - the plain text, or the HTML converted to text when there is none, capped at 20k characters;
     - the `application/ld+json` scripts of the HTML body;
     - attachment names.
  3. **Classify.** One OpenRouter chat completion per message, with `response_format` set to a JSON schema and `provider: {data_collection: "deny"}`. It is given the subject, sender, the first 4k characters and the JSON-LD.
     - It returns `{category, summary}`. `category` is one of `bill`, `travel`, `order`, `appointment`, `commitment`, `person`, `other` or `skip`, and `summary` is one line.
     - `skip` is for newsletters, notifications, one-time and security codes, and promotions the Gmail category missed.
     - The model only classifies. What to save, and how it relates to memory, is left to Roamind.
  4. **Label.** Each message gets `roamind/<category>`. A `skip` message also gets `roamind/skip` and isn't sent.
  5. **Send.** It POSTs `{address, message_id, thread_id, link, from, to, cc, date, subject, category, summary, text, jsonld, attachments}` to `WEBHOOK_URL`. The `X-Roamind-Signature` header is the hex HMAC-SHA256 of the body with `WEBHOOK_SECRET`. On a 200 the message gets `roamind/sent`. Otherwise it is left unlabeled and sent again on the next run, so mail sent while the machine is down is delayed, not lost.
  6. **Heartbeat.** Once a day, when nothing else was sent that day, it POSTs `{address, heartbeat: true}`.
- When OpenRouter fails, the message is left for the next run. The run logs to the Apps Script executions page and never throws for one bad message.

### 2. The webhook: `gmail`
- `worker/cmd/setup-webhook` also creates the `gmail` webhook, when `ROAMIND_MAIL_SECRET` is set. Like the Slack one, it is recreated so the secret follows `.env`. It prints the URL to put in `WEBHOOK_URL`.
  - The source is `rest.GENERIC`.
  - Auth is `features.HMACAuth{SigningSecret: ROAMIND_MAIL_SECRET, SignatureHeaderName: "X-Roamind-Signature", Algorithm: rest.SHA256, Encoding: rest.HEX}`.
  - The event key expression is `'mail:received'`.
- `task funnel` is unchanged: its path prefix already covers every webhook.
- There is one secret per deployment, shared by every account's script. The worker accepts only addresses registered in `mail_accounts`.
- Hatchet's generic HMAC carries no timestamp, so a request could be replayed. A replay is harmless: `check` drops messages already handled.

### 3. Data: `memory/sql-schemas/11_mail.sql` + `memory/store/mail.go` (+ `mail_test.go`)
- `mail_accounts(address PK, user_id, connected_at, seen_at)`. `seen_at` is updated by every request from the account's script, heartbeats included.
- `mail_messages(address, message_id, user_id, status picked|done|skipped|failed, category, received_at, done_at, PK(address, message_id))`. A message is recorded before its turn, so a resent message isn't related twice. This follows the `job_postings` pattern.
- Store functions keep the repo's short names: `MailAccount`, `SeenMail`, `PickMessage`, `FinishMessage`, `StaleMailAccounts`.
- **`task mail-connect -- <roamind user email> <address>`**: `sqlite3 INSERT OR REPLACE` into `mail_accounts`, with `user_id = sha256(lower(trim(email)))`. The task refuses if no such user exists. A user may have many accounts; an address belongs to one user.
- **`task mail-disconnect -- <address>`** deletes the row. The operator also removes the script's trigger in that account.

**What mail turns into:**

| Mail | Note | Item row |
|---|---|---|
| A bill or invoice to pay | Task "Pay \<payee> \<invoice number>", with `due` and `amount` | `remind_on` is `due` minus 3 days; the row closes as done when paid |
| An order | Event, `kind: delivery`, dated by the expected delivery, with the tracking `link` | closes as done when delivered, or as cancelled |
| A flight, train, stay, ticket or appointment | Event with its `kind`, `confirmation` and `checklist` | closes as cancelled when cancelled |
| A commitment ("I'll send it by Friday") | Task | |
| A new person the user deals with | Person, with the address as a `contact` | |

- **Automatic renewals aren't captured for now.** Event has no `renewal` kind; a renewal that needs action, such as cancelling a subscription in time, is a task.
- **Return windows get no reminder.** Most orders aren't returned, so one reminder per order would be mostly noise. The delivery event's body records the window ("returnable until 2026-11-20"). When the user says they want to return something, the agent creates a task due that day.
- Rows from mail have `source` `mail:<address>/<message id>`.

### 4. Workflow: `mail-message` (`worker/workflows/mail_message.go`)
Triggered by the `mail:received` event. It is serialized per user (concurrency key `user_id`), so one user's bm writes never overlap.

1. **`check`** (no turn):
   - It looks up the address with `MailAccount` and drops the request if the address isn't registered. Otherwise it calls `SeenMail`.
   - A heartbeat stops here.
   - So does a message `PickMessage` has already recorded. A new message is recorded as `picked` with its category.
2. **`relate`**: one agent turn (`agent/prompts/relate_mail.md`) with structured output. The mail is third-party text, so the turn gets `ToolsRecall`, as the inbox digest's draft does: it can read memory and search the web, and can't change notes, files or items.
   - **Input:** the user's name, email, timezone and today's date; their active goals (title and permalink, from `notes.Goals`); the message's headers, the script's category and summary, the text and the JSON-LD.
   - **What it does in memory:**
     - It searches for what the mail is about: the note of the same booking, order or invoice (by its reference number), the sender's person note, the goal or event it belongs to, and an earlier commitment it fulfils.
     - It reads the user's preferences for that kind of event, for checklists.
   - **Output:** either `skip` with a reason, or a list of items.
     - Each item has a `kind` (task, event, person), the fields of that schema, a `key` and a `state` (`open`, `done` or `cancelled`, from what the mail shows).
     - The key is a stable title: a confirmation, order or invoice number when there is one. When memory already has a note for the same thing, it is that note's title, so the update lands on it.
     - Tasks can carry a `remind_on`; bills always do. Events carry their `checklist` lines.
     - Each item lists `relations`: the permalinks of the goals, people and events it relates to, each with the relation's name (`part_of`, `with`, `for`).
   - Retried once.
3. **`save`**: for each item, the harness does two writes.
   - **The note.** It writes the item as Markdown with `notes.WriteNote`, through bm's CLI, as exercise `post` does.
     - Notes are titled by `key`, so a later mail about the same booking or order (rescheduled, shipped, cancelled) overwrites its note with the latest details.
     - Relations are written as the note's wiki-link relations, which ties the mail into the knowledge graph.
     - Task and Event have no `source` field. The note's opening text names the account, the message's Gmail link and the subject, and the row's `source` column records the message id. Its evidence is `(observed, email, <date>)`.
     - Person items are written only when no note in `people/` already lists that address as a `contact`. This is a disk read in `memory/store/notes`.
   - **The rows.** For each task and event, it calls `PutItem` with the note's permalink, its dates through `store.InboxLeads`, and the `state` the turn reports: a cancelled booking, a paid bill, a delivered or cancelled order.

   Then it calls `FinishMessage(done)`; a message whose turn skips it is marked `skipped`. Retries are safe, because notes overwrite by title and rows upsert by key.
- An on-failure task marks the message `failed`. The script has already labeled it `roamind/sent`, so the mail is not sent again; the run's input stays in Hatchet for a replay.
- Wiring goes in `workflows.go` `Register` (only with Memory) and in the package doc list.

### 5. `inbox-digest` changes
- `check-skip` also picks the user's `StaleMailAccounts`: those with no request for two days. The day is not skipped while one is stale.
- The draft prompt lists them as "mail from \<address> hasn't arrived for two days; check its Apps Script".

### 6. Prompt and agent code
- `agent/mail.go`: the `MailMessage` input, `RelateMailPrompt`, `RelatedMailSchema` (JSON schema, descriptions carry the field formats), and Go types, following `agent/jobs.go`.
- `agent/prompts/relate_mail.md`: positive instructions.
  - Give the data (user, date, goals, message, the script's category).
  - Say how to search memory for the thing the mail is about, and to reuse an existing note's title as the key.
  - Say what to capture and how to date it: absolute ISO dates in the user's timezone. Treat JSON-LD as the most precise source.
  - Ask for one item per real-world thing, keyed by its reference number.
  - Say how mail maps to the types, as in the table in section 3.
  - Treat the script's category as a hint: the turn may skip the mail, or find it holds more than one item.

### 7. Docs, config
- `docs/architecture.md`: a new "Email assistant" section with the flow diagram above.
- `docs/deployment.md`:
  - an OpenRouter key with credit;
  - `clasp login` and `task mail-script` per Gmail account, and running `setup()` once;
  - the script properties;
  - `task mail-connect`;
  - `ROAMIND_MAIL_SECRET` and `task webhook`.
- `README.md`:
  - the inbox digest's feature line names mail as a source;
  - the public-surface principle names the `gmail` webhook beside Slack's;
  - a design principle: "Read Gmail with an Apps Script in each account, so Roamind needs no Gmail OAuth app…" (per README voice).
- `.env.example`: `ROAMIND_MAIL_SECRET`.
- **Privacy note in the docs:**
  - The first 4k characters of each mail not filtered out by Gmail's categories go to OpenRouter, served by hosts that don't keep data.
  - The mail worth keeping then goes to Roamind and Claude.
  - Hatchet keeps each event's payload, mail text included, for its retention period.
  - Memory stores only the notes, not the raw mail.

## Considered

### Reading Gmail from Roamind (the plan of 2026-10-22)
A `mail-sync` cron would poll each account through the Gmail API (`connectors/mail`), with an OAuth Desktop client and `oauth2l` for connecting, and `historyId` sync. It was dropped for the script:
- it needs a Google Cloud OAuth app on the restricted `gmail.readonly` scope, kept "In production" so refresh tokens don't expire;
- it needs a Gmail client and sync state in Roamind;
- it can't label mail.

### Gmail push through Pub/Sub
Gmail can push new-mail notifications through Pub/Sub to a webhook. It was rejected for the earlier plan, and the script makes it unnecessary:
- A push carries no mail, so the triggered run still has to read it from the Gmail API.
- Pub/Sub signs each request with a new OIDC token, and Hatchet's webhooks can't verify one. The script signs with HMAC, which Hatchet checks.

### Extracting items in the script
The script could have DeepSeek extract the full items and Roamind only save them. It was rejected:
- an extraction made without memory can't tell an update to an existing booking from a new one;
- it can't link items to goals and people;
- the schema and prompt would live outside the repo's Go types and tests.

## Files
New:
- `deploy/apps-script/{Code.js,appsscript.json,.clasp.json.example}`
- `memory/sql-schemas/11_mail.sql`, `memory/store/mail.go` and its test
- `agent/mail.go`, `agent/prompts/relate_mail.md`
- `worker/workflows/mail_message.go` and its test, with `testdata/relate_mail.jsonl`

Changed:
- `worker/cmd/setup-webhook/main.go` (the `gmail` webhook)
- `worker/workflows/{workflows.go,inbox_digest.go}`, `agent/inbox.go`, `agent/prompts/inbox_digest.md` (stale accounts)
- `Taskfile.yml` (`mail-connect`, `mail-disconnect`, `mail-script`), `docs/*`, `README.md`, `.env.example`

Reused:
- `notes.WriteNote` and `notes.Goals` (`memory/store/notes/`)
- `PutItem` and `InboxLeads` (`memory/store/items.go`)
- `ToolsRecall` structured turns (`worker/workflows/inbox_digest.go`)
- the Slack webhook's setup (`worker/cmd/setup-webhook`) and Funnel path
- `storetest` for the store tests

No new Go dependencies. All work goes in one branch and one PR.

## Verification
1. **Unit tests:** `go test ./...` in `memory`, `agent` and `worker`. This covers:
   - store CRUD for `mail_accounts` and `mail_messages` on the real SQL files;
   - `mail-message` with a recorded `relate_mail.jsonl` turn writing the expected notes, relations and rows. A second mail with the same key reschedules the row, a cancellation closes it, and a resent message is dropped by `check`;
   - an unregistered address and a heartbeat, neither of which starts a turn;
   - `inbox-digest` listing a stale account, and not skipping the day for it.
2. **Build and init:** `task build && task init`, then check the mail tables in `data/roamind.db`.
3. **Webhook:** `task webhook` prints the `gmail` URL. A `curl` with a wrong signature gets 403 through the Funnel; one signed with `ROAMIND_MAIL_SECRET` creates a `mail:received` event (`hatchet` skill: trigger-and-watch).
4. **Live run:**
   - Run `task mail-connect -- sainageswar@gmail.com <address>` and `task mail-script` for two accounts, then `setup()` in each.
   - After a trigger run, check the Gmail labels.
   - Then check the `mail_messages` statuses, the `items` rows, and the new notes under `data/memory/users/<p>/notes/{tasks,events,people}`. A bill should be a task with `amount`, and an order a delivery event with its tracking link, related to its person or goal notes.
   - Run bm `schema_validate` on the notes.
   - The next morning's inbox digest should show the mail's items that are due.
5. **Failure path:** delete one account's trigger. Two days later the digest should say its mail hasn't arrived.
