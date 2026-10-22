# Architecture

How Roamind works inside. For an overview, see the [README](../README.md).

## How it works

Slack sends events to Hatchet's Slack webhook. Each message becomes one `chat-turn` run, which drives `claude -p` and streams the reply into the Slack thread by editing a message. A user who is not set up yet, a new one above all, gets a `user-setup` run instead of a turn, and a reply asking them to write again in a minute. Claude talks to Basic Memory's own MCP server, pinned to the user's project. After each turn, the harness checks whether any active goal still needs a channel.

```
Slack ─HTTPS─▶ Tailscale Funnel (webhook path only) ─▶ Hatchet ─event slack:chat─▶ worker: chat-turn
                                                                                         ├ identity (sha256(email))
                                                                                         ├ not set up? ─▶ user-setup run (not waited for) + "setting things up" reply; the run ends
                                                                                         ├ thread → claude session (SQLite)
                                                                                         └ claude -p --resume … → throttled chat.update
                                                                                              └ stdio: bm mcp --project bm-u-<name>-<n>
                                                                                         └ after the turn: an active goal without a channel? ─▶ goal-channels run
                                                                                              └ conversations.create (private #goal-<slug>) → invite owner → intro
```

## Modules (`go.work`)

| Module | What it holds |
|---|---|
| `agent` | `claude -p` runner and stream-json decoder; packet translation; Go port of onyx's citation processor; `cmd/turn` dev CLI. `agent/protocol` is the contract with the Slack layer: packet types (onyx-style), turn input/output and the snapshot accumulator. It's a separate package with no dependencies, so the Slack updater imports it without the agent runtime. `mcp_servers.go` builds the turn's `--mcp-config` (Basic Memory's `bm mcp`, pinned to the user's project, and Roamind's `bin/mcp`) with the tools allowed and disallowed on them |
| `channels` | Where users talk to Roamind: Slack (`slack`: event types, mrkdwn renderer, streaming updater), the web app (`web`: its Firestore and Storage), and the email-hash identity they share (`identity`) |
| `connectors` | What pulls content in from outside, one package per kind, after Onyx's connectors: the RSS/Atom feed reader (`feeds`), job postings from applicant tracking systems (`jobs`: one file per ATS implementing `jobs.ATS`, `Match` and `Posting`; `Parse` tries the `systems` list in order, and a URL none matches is read from its page), and the public-only HTTP client both use for URLs that come from the agent or the web (`safehttp`), with a public-only proxy for the headless Chrome that renders posting pages. It depends on no other Roamind module |
| `worker` | The Hatchet worker, laid out like Hatchet's Go quickstart: `cmd/worker`, `cmd/setup-webhook`, `client` (the Hatchet client) and `workflows`, one file per workflow with its tasks and wiring: `chat-turn`, `goal-channels`, `learning-exercises`, `learning-exercise`, `user-setup`, `news-shorts`, `news-digest`, `news-reaction`, `job-search`, `job-hunt`, `job-application` |
| `memory` | Roamind's data: the files below, `store` (the Go code that reads and writes the SQLite database) and `store/notes` (users' Basic Memory notes) |
| `mcp` | Roamind's MCP server, which claude starts per chat turn over stdio, pinned to the turn's user: `cmd/mcp` (`bin/mcp`) and `tools`, one file per tool set (`interests.go`: following and adding news interests; `jobs.go`: asking the job assistant to draft applications), served by name with `--tools`. Built on the official Go MCP SDK |

`memory/` holds the data: its definitions, seed data and the Go code over it. The data files are read from disk, not embedded. `task init` (process-compose runs it before every worker start) applies them with the `sqlite3` CLI, `jq` and `cp`; each step is safe to repeat. The worker reads `basic-memory-config.json` and `basic-memory-schemas/`, at `ROAMIND_MEMORY_DIR` (default `./memory`), to set up new users' projects.

- `sql-schemas/*.sql`: the SQLite store's tables, one file per area, applied in name order by `task init`. The worker and `bin/mcp` only open the database; the worker refuses to start when it has no tables.
- `store/`: the models and queries of those tables, one Go file per schema file (`01_users.sql` → `users.go`, and so on), used by the worker and `bin/mcp`. Tests create the tables the way `task init` does (`store/storetest`).
- `store/notes/`: the users' Basic Memory notes on disk: the project layout and names, creating a project (`Setup`: its folders, `config.json` and schema notes), reading goal and exercise notes, and writing notes through bm's CLI.
- `seed/`: seed data as SQL. `interests.sql` inserts the curated news interests, each reading its rubric from `rubrics/*.md` with sqlite3's `readfile`.
- `basic-memory-config.json`: the bm settings Roamind manages (search models), written into each project's `config.json` by the user-setup workflow for a new project and by `task init` for existing ones. bm's own keys are kept.
- `basic-memory-schemas/*.md`: the Picoschema notes copied into each user's Basic Memory project, by user-setup for a new one and by `task init` for existing ones. They are the repo's: a project's copy that differs is replaced.

## Design rules
- Conversation history lives only in claude's session files (`~/.claude/projects/<cwd>`). SQLite maps each thread to its session id and stores nothing else about the conversation.
- Chat turns run in `data/users/<user_id>/workspace`, so `--resume` can find the session.
- **A turn gets the tools its input warrants** (`TurnInput.Tools`). Chat turns, exercise drafting and importing a resume uploaded in the web app get the workspace set below (`ToolsWorkspace`): files users upload, in Slack or the web app, are their own. The job assistant's web search gets `ToolsWeb` (WebSearch and WebFetch only, with the blocked domains denied). News ranking and the job assistant's ranking and tailoring, whose input is third-party text, get no tools (`ToolsNone`), in a throwaway directory with no saved session. Which of Roamind's own tool sets a `ToolsWorkspace` turn gets is configured per task (`TurnTools` in `worker/workflows/turn_tools.go`); see below.
- **Each user has a sandboxed workspace.** Claude gets web search and fetch, the file tools and Bash, working in `data/users/<user_id>/workspace`. That directory persists across conversations and is private to the user. See `agent/workspace.go`:
  - **File tools:** `permissions.blockReadsOutsideWorkingDirectories` stops reads outside the workspace. Edits are allowed only by an `Edit(//<workspace>/**)` rule, and in `dontAsk` mode anything not allowed is denied, so a bare `Write`/`Edit` rule would have allowed any path.
  - **Bash** runs in Claude Code's bubblewrap sandbox (`bwrap` and `socat` must be installed). It can write only to the workspace and `$TMPDIR`. It can't read the home directory, which covers other users' workspaces and memory, `data/`, `.env` and `~/.claude`. `allowUnsandboxedCommands: false` and `failIfUnavailable: true` mean nothing ever runs unsandboxed.
  - **Network:** `WebFetch(domain:*)` opens the web to WebFetch and to sandboxed commands. The sandbox proxy still refuses local addresses, so Hatchet's ports are unreachable from the sandbox.
  - **Files from Slack:** files shared with a DM (`subtype: file_share`) or a mention are downloaded with the bot token (scope `files:read`, up to 50 MB each) into `uploads/` in the workspace. The message the agent sees ends with a list of them, and Claude's Read tool reads PDFs, images and text directly.
  - User settings and ambient MCP servers are still ignored (`--strict-mcp-config`, `--setting-sources ""`); the turn's own `--settings` carries the rules above.
- A thread belongs to the user who started it. Other users in that thread are asked to start their own.
- Runs in the same thread are serialized by the Hatchet concurrency key `channel:thread_ts`.
- Runs started from webhook events can't carry custom run metadata. To find a conversation's runs, use the run output (`thread`, `user_id`, `session_id`).

## Memory
- Each user has one Basic Memory project, `bm-u-<slugified display name>-<n>`. The `-<n>` keeps names unique.
  - The name is assigned by the user-setup workflow and stored in `users.memory_project`.
  - It's read back with the user row, so nothing is computed per turn, and it doesn't change if the user is renamed.
- Every turn starts `bm mcp --project <that project>` over stdio through `--mcp-config`. As a second layer, bm enforces the pinning itself: it ignores `project` arguments and `search_all_projects`, and it refuses to create or delete projects.
- The agent has full read/write access to its project: every bm tool is allowed (`--allowedTools mcp__memory`), including delete and move. Only project and workspace management (`list_memory_projects`, `create_memory_project`, `delete_project`, `list_workspaces`) is passed to `--disallowedTools`, since `--project` refuses those anyway.
- **Search models on the GPU:** Basic Memory's hybrid search uses Qwen3-Embedding-0.6B (1024-dim) and re-ranks with Qwen3-Reranker-0.6B, both served by vLLM on the local GPU (`embed` on 127.0.0.1:8800 and `rerank` on 127.0.0.1:8801 in process-compose; `deploy/bin/run-vllm.sh`).
  - Each takes 35% of an 8 GB card, with a 4096-token context.
  - bm reaches them through its `litellm` provider, configured by `memory/basic-memory-config.json`, which user-setup writes into a new project's `config.json` and `task init` into existing ones.
  - Search queries get a Qwen3 instruction prefix, and the re-ranker's default instruction is set for our notes in `deploy/vllm/qwen3_reranker.jinja`.
  - Changing the embedding model makes bm re-embed existing notes.
- **Each user has their own Basic Memory setup and database.** Under `data/memory/`, separate from any personal `~/.basic-memory`:
  - `fastembed_cache/` holds the embedding model, downloaded once and shared read-only;
  - `users/<project>/config/` holds that user's `config.json` (listing only their project) and `memory.db` (index, full-text search and embeddings);
  - `users/<project>/notes/` holds their Markdown notes.

  Each turn's bm gets only that user's `BASIC_MEMORY_CONFIG_DIR`, so it can't open another user's database. To delete a user's memory, remove `users/<project>`. Notes are plain Markdown, so you can browse and edit them directly.
- New projects are seeded with the Picoschema notes in `memory/basic-memory-schemas/*.md` (person, place, preference, event, goal, progress, task, exercise). Add or edit a file there and every project gets it at the next `task init`; a project's own edits to a schema note are overwritten. The prompt tells the model which folder and `note_type` to use for each.
- **Preferences are atomic:** one note per preference in `preferences/<subject>/`, with exactly the schema fields `[preference]`, `[subject]` and `[evidence]`.
  - `subject` is an enum: food, travel, fitness, health, work, tech, communication, shopping, entertainment, home, finance, other.
  - `evidence` is an enum: stated, inferred, observed or corrected. The source and date go in its context, e.g. `- [evidence] stated (slack, 2026-10-11)`.
- **What `schema_validate` checks:** that required fields are present and enum values are allowed, with warnings rather than errors. It doesn't check types, formats (dates, coordinates), cardinality or the target type of a relation; the prompt carries those rules.
- **Required fields:** a person's `relationship` (`self` for the user's own note, exactly one per project), a place's `kind`, an event's `date` (as precise as known), and a goal's `status` and `outcome`.
- **Person** is kept small: `relationship`, `contact`, `works_at` (a link to a Place), and the career fields `experience`, `education` and `interest`.
  - The user's own resume goes on their `self` note.
  - Each role or degree is one line in a fixed format, so its parts stay together, e.g. `- [experience] Postdoctoral researcher, ETH Zurich, 2021-09 to present: highlights (stated, slack, 2026-10-11)`.
  - The current job ends with `to present`.
  - These formats are enforced only by the prompt.
- **Goals, progress and tasks:** a Goal note (`goals/`) is anything worked on over time: a project being built (`kind build`), something being learned (`kind learn`) or another aim (`kind personal`).
  - It holds only what defines the goal: `status`, `outcome`, `kind`, `target_date`, `why`, `link`, `with` and, for learning goals, `exercise_preference`.
  - **Progress:** each update is its own note in `progress/<goal>/` (`date`, `summary`, `next`, `blocker`), linked back with `progress_of`.
  - **Tasks:** optional. A task linked to a goal via `task_of` is a note in `tasks/<goal>/` (`due`, `amount`, `with`); a task with no fitting goal goes in `tasks/`. A bill or invoice to pay is a task titled "Pay <payee> <invoice number>" with `due` and `amount`.
  - Progress and goal tasks link to the goal, so a goal accumulates updates and tasks without its own note growing.
- **Events** are one note per dated thing, in `events/<YYYY>/` by the year of their `date`. Their `kind` covers appointments, travel, stays, tickets, meetings, deliveries (an order, dated by its expected delivery, with its tracking `link`), milestones and other. An event whose date moves to another year is moved to that year's folder. Bookings and orders keep their reference in `confirmation`.
  - **Checklists:** an event's `checklist` lists what to bring or prepare. It starts from the user's reusable lists, which are preferences ("what I always pack for a flight" is one preference, with the whole list comma-separated in its `preference` line).
- **Item state lives in SQLite, not in the notes.** Task and Event notes have no status. Each active one, a task still to do or an event still ahead, has a row in the `items` table (`memory/sql-schemas/09_items.sql`, `memory/store/items.go`), keyed per user by a stable title or a confirmation, order or invoice number, which owns its state (`open`, `done`, `cancelled`, with `closed_at`) and the days it shows as due:
  - **`show_from`** is `remind_on` when given, else the day before the date (a task's `due`, an event's `date`), and the date itself for deliveries. An undated item never shows.
  - **`until`** is empty for tasks, so overdue tasks stay until closed; it is the date for deliveries, and `end`, else the date, for other events. `store.InboxLeads` computes both once, when the row is written.
  - `DueItems(user, day)` answers "what is due today" with one indexed query, which bm can't: a note's fields are body lines its filters don't see. `Items` serves chat, by state, date range, note folder or key. Closed rows stay as history.
  - Chat writes rows through the `items` tools of Roamind's MCP server (see News shorts); the model writes or edits the note first, then saves its row with the note's permalink. Notes written before the table keep their old `[status]` lines as plain observations, with no row.
- **Links between types:** people link to workplaces with `works_at`. Events link to people and places with `with` and `at`, and goals link to people with `with`.
- **Other notes** (people, places, events) are one note per thing. Each fact ends with its evidence as Basic Memory observation context, e.g. `- [interest] oscillatory neural networks (stated, slack, 2026-10-11)`. bm stores that context in the observation's `context` field.
- **Links between notes** are relations written as `- works_at [[places/ibm-research-zurich]]`, using the target's permalink so the link is exact.
  - bm indexes each one as a row in its `relation` table, with `to_id` as a foreign key to the target note. A link to a note that doesn't exist yet stays unresolved (`to_id` NULL) until that note is created.
  - People link to Place notes with `works_at`. Places hold `location` (the address) and `coordinates` (`lat,lon`, WGS84 decimal degrees, saved only when the user gives them).
  - Schema notes are indexed like any other note, so their text must not contain fact lines (`- …`) or `[[…]]`. Those would become facts and links in every user's memory. `TestSchemasHaveNoFactsOrLinks` enforces this.
  - The schema header must be valid YAML; bm silently ignores a schema it can't parse, so quote values that contain `: `. `TestSchemaFrontmatterParses` enforces this.
- **Evidence** says how a fact was learned (`stated`, `inferred`, `observed` or `corrected`), plus the source (`slack`) and date.
- The agent decides what to save; the rules are in `agent/prompts/system.md`.

## User setup
`user-setup` (`worker/workflows/user_setup.go`) sets up one user, in three tasks, each needing the one before:

```
memory-project → bm-setup → news-channel
```

- **memory-project** assigns the user's Basic Memory project name (`users.memory_project`).
- **bm-setup** creates that project with `notes.Setup`: its folders, its `config.json` from `memory/basic-memory-config.json` and the schema notes from `memory/basic-memory-schemas/`.
- **news-channel** creates the user's news channel and has them follow the default interests (see News shorts).

Every task is safe to repeat. A user has one run at a time: a run started while one is in flight is cancelled (`CancelNewest`), since that one does its work. Runs are top-level, not children of the run that started them, so every setup shows on its own in the dashboard. Nothing waits for it, so it never holds up a turn or a worker slot:

- `chat-turn` starts it when the user's memory project isn't ready (a new user, a corrupt `config.json`, or a setup that failed or is still running): it replies that it is setting things up, asks them to write again in a minute, and ends the run. If the run can't even be started, it says so instead.
- When only the news channel is missing, `chat-turn` starts it and still runs the turn.
- `news-shorts` starts it for users without a news channel.

The environment the projects share (the embedding cache, settings and schema notes of existing projects) is `task init`'s.

## Goal channels
- **What gets a channel:** every Goal note (`goals/`) whose `status` is `active` gets a private Slack channel, `#goal-<slugified title>`. A goal saved without a status counts as active.
  - If the name is taken, the channel gets a suffix: `-2`, `-3`, …
  - A paused goal gets its channel when it becomes active again.
  - Achieved and dropped goals get none.
- **When it's created:** after every chat turn, `goalChannelsTrigger` compares the user's goal notes on disk (`notes.Goals`) with their goal channels in the `channels` table. This needs only file reads and SQLite, so most turns start nothing.
  - When an active goal has no channel, it starts a `goal-channels` run (`worker/workflows/goal_channels.go`).
  - That run creates the channel, records it, invites the owner and posts an intro.
  - Runs are serialized per user and retried up to 3 times. The row is saved right after the channel is created, so a retry never creates a second channel.
  - A goal that already exists gets its channel on the user's next message anywhere.
- **The `channels` table** maps every channel the harness makes to its user and purpose: (user, `kind`, `ref`) to a Slack channel id, where `kind` is `goal` (with the goal's permalink as `ref` and its title) or `news` (with an empty `ref`). It is managed only by the harness; the agent is never told about channels. A channel has one purpose per user (unique on channel and user), so chat-turn routing makes one lookup (`ChannelOf`) and switches on its kind. Every channel is made by one helper, `openChannel` (`worker/workflows/channels.go`): it creates the private channel (with a `-2` suffix if the name is taken), records it before anything else, so a retry never makes a second one, invites the user (again on a retry, so a failed invite is made up) and posts the intro.
- **Shared goal channels:** several users can share one channel, each linked to the goal in their own memory. Each user has their own row, with a unique key on (channel, user). In the channel, each user's messages run as their own turns, with their own memory and their own goal note, so memory stays isolated per user. To share a channel, point the other user's row for their goal at it; they can then archive their own channel.
- **Chatting in a goal channel:** plain messages from any user with a goal in the channel start turns, with no @mention needed.
  - Slack also sends an `app_mention` copy of a message that mentions the bot; that copy is skipped, so each message runs once.
  - The turn's `protocol.GoalRef` adds a paragraph to the system prompt naming the goal and its permalink. That paragraph asks the agent to load the goal with `build_context` and to link new progress and tasks to it.
  - As everywhere, each top-level message starts its own thread and claude session.
- **Other private-channel messages:** the webhook routes every `message` event from a private channel (`channel_type == group`) to `slack:chat`. `chatturn.route` then skips the ones that aren't the owner's messages in their own goal channel. Other people in a goal channel, and anyone in other private channels, reach the agent only by @mentioning it, and those turns carry no goal.
- **Archiving:** the harness never archives or deletes channels; users archive them. An archived goal's row stays, so its channel isn't re-created.
- **Slack scopes:** `groups:write` (create private channels and invite people), plus `groups:history` and the `message.groups` event for reading them.

## Daily exercises (learning goals)
- **What gets one:** every active goal with `kind learn` that has a channel, at 9:00 in `ROAMIND_TZ` (default: the server's zone).
- **Scheduling:** Hatchet crons run in UTC, so `LocalCrons` registers the UTC times of 9:00 local in winter and in summer, for example `0 8 * * *` and `0 7 * * *` for Europe/Berlin. The `learning-exercises` cron task does nothing unless it is 9:00 local. Otherwise it starts one `learning-exercise` run per learning goal, as child runs; a DAG can't fan out to a varying number of goals.
- **The `learning-exercise` DAG** (`worker/workflows/learning_exercise.go`), serialized per goal:
  1. **`check-skip`** returns a reason to skip when the goal isn't an active learning goal with a channel, today's exercise is already posted, or the latest exercise is unanswered. The latest exercise blocks every later day until the user replies in its thread; answering late resumes exercises the next morning. Hatchet still runs the children of a skipped task, so every later task lists `check-skip` as a parent and skips itself on that reason.
  2. **`build-context`** reads the goal note, with its `exercise_preference` lines, and the latest five exercise notes, with the user's answers and results. It reads them from disk, because bm's `build_context` lists the linked notes but not their content.
  3. **`draft-exercise`** runs an agent turn with that context (`agent/prompts/draft_exercise.md`). Its structured output (`claude -p --json-schema`) is topic, difficulty, format and the exercise text. The turn has the workspace tools and the user's memory, so it can draw on their uploads and notes as well as the web. It is retried once.
  4. **`post`** posts the exercise as a new top-level message in the goal channel, with overflow in its thread. It then records the thread → drafting session and the `exercises` row, and writes the Exercise note at `exercises/<goal>/<date>` with `bm tool write-note`. It has no retries, so an exercise is never posted twice.
- **Answering** is not part of the workflow. When the goal's owner replies in an exercise thread, chat-turn sets `answered_at` and resumes the drafting session, so the agent knows exactly what was asked. The system prompt names the exercise note; the agent evaluates the answer, appends it to the note under Answer, and adds `result`, `score`, `feedback` and `next`.
- **Exercise preferences** are `exercise_preference` lines on the goal note, saved by the agent when the user states them in the goal channel.

## News shorts
- **The channel:** user-setup's `news-channel` task (`worker/workflows/user_setup.go`) creates the private `#news-shorts-<name>` channel (with a `-2` suffix if the name is taken), with `openChannel` (see Goal channels), as a `news` row of `channels`, makes them follow the catalog's default interests and posts an intro. New users get theirs from user-setup when they first write; a user whose channel failed gets it at their next turn or from the next `news-shorts` run.
- **The interest catalog** is the `interests` table: each interest with its curated RSS/Atom feeds (`sources`) and its `rubric` in prose: two to four separately judged criteria scored 0 to 10, with what each band of scores means, when an item is not to be included at all, and optionally the categories to file items under. The curated ones are the `INSERT` rows of `memory/seed/interests.sql` (name, `is_default` 1 for the ones every user follows, rubric, feeds), each rubric in `memory/seed/rubrics/*.md`. `task init` runs it: rows are added or updated, and curated interests no longer listed are deleted with their follows. Edit those files and restart the whole service (`systemctl --user restart roamind`, or `task init` then restart the worker) to change them; restarting only the worker doesn't rerun `init`. Interests the agent adds belong to the user they were added for (`owner`): each user sees the curated interests and their own, never another user's. Users follow interests by id (`user_interests`, active or muted).
- **Roamind's MCP server** is how the agent changes what a user follows. Its tools come in tool sets, and which ones each task's turns get is configuration: `TurnTools` in `worker/workflows/turn_tools.go` maps a workflow name to its tool sets (chat turns: `interests`, `jobs` and `items`; exercise drafting and resume import: none). The worker leaves out a set it can't serve (`jobs` and `items` without memory), and sets `TurnInput.ServerTools`; a turn with none gets no server. Otherwise claude starts `bin/mcp --data <dir> --user <id> --tools <sets>` over stdio as the `roamind` server, next to bm, pinned to the turn's user the way bm is pinned to a project (`mcp/cmd/mcp`, built on the official Go MCP SDK); it adds each named set from its registry (`tools.Register`) and refuses an unknown one. Adding a tool set is a file in `mcp/tools`, an entry in its registry, and its name in `TurnTools` for the tasks that get it. The worker finds `bin/mcp` next to its own binary, or at `ROAMIND_MCP_BIN`. Its interest tools (`mcp/tools/interests.go`):
  - `list_interests`: the user's catalog, curated and their own, with feeds and rubrics, and what the user follows.
  - `follow_interest`: follow an interest of their catalog, or resume a muted one.
  - `add_interest`: add an interest of the user's own, with its feeds and a prose rubric in the same style, and follow it. Every feed is fetched and must parse as RSS or Atom.
  - `set_interest_status`: mute or resume.
  Its item tools (`mcp/tools/items.go`), in every channel, track the state of tasks and events (see Memory):
  - `save_item`: add a row for a task or event note, or move its dates; the tool computes `show_from` and `until` from the dates the model gives (`date`, `remind_on`, `end`). Rescheduling keeps the row's state.
  - `close_item`: `done`, `cancelled`, or `open` to reopen.
  - `list_items`: the open items by default; by state, date range, note folder (`tasks/<goal>/` for a goal's tasks) or key.
  In the news channel, the owner's plain messages are news turns, and their prompt lists what the user follows.
- **Scheduling:** the `news-shorts` cron runs once a day at 16:00 in the server's zone (`ROAMIND_TZ` overrides it; `NewsHours`; add hours there for more digests a day). At a slot it starts one `news-digest` run per user with a channel, and a `user-setup` run for each user without a channel yet. A run that can't be started is logged and listed in its output; the others still start.
- **The `news-digest` DAG** (`worker/workflows/news_digest.go`), serialized per user, with the same skip pattern as `learning-exercise`:
  1. **`check-skip`** skips users without a channel or active interests, and slots already posted (`digests` is unique per user and slot).
  2. **`gather`** fetches every feed of the user's active interests once, in parallel (`connectors/feeds`, gofeed). Feed URLs can come from the agent and are fetched outside the sandbox, so the reader connects only to public addresses (loopback, private, CGNAT/tailnet, link-local and multicast are refused when dialled, which covers redirects and DNS) and reads at most 8 MB. It keeps items from the last 30 days (slow feeds such as Stoic writing post a few times a month), the newest 3 per feed, each link once, drops links already sent to the user (`sent_urls`), and keeps the newest 25 per interest, so the rubric does the filtering. A feed that fails is logged and skipped; if every feed fails, the task fails and is retried.
  3. **`rank`** runs one agent turn per interest (`agent/prompts/rank_digest.md`), three at a time, with structured output and no tools at all, in a throwaway directory with no saved session: the candidates are third-party text, so nothing in them can steer the turn at the user's files (`protocol.ToolsNone`). From each item's title, source, date and feed summary, and the slot's date as the coverage day, it returns per candidate: a 0-10 score for each criterion of the interest's rubric, `include` (whether it belongs in the digest at all), a `category` when the rubric has categories, a `story` slug naming the underlying event, and a short summary. Each turn is given the stories sent in the last 48 hours, slug and title, so the same event keeps its slug. An interest whose turn fails is left out of that digest; the task fails, and is retried, only when every interest fails.
  4. **`post`** keeps the included items whose combined score, the mean of their scores, is at least 5, nudged by feedback, and drops stories sent in the last 48 hours. It posts the best 4: one per story, at most 2 per interest and 1 per category. The message is plain: a header, then per item its title linked to the source, the summary and the interest, with link previews off. It reserves the slot (the `digests` row, unique per user and slot) before posting, then records the message, its items, and their links and stories as sent in one transaction, so a replayed run never posts twice; a failed Slack post gives the slot back. When nothing qualifies, nothing is posted. No retries.
- **Dedupe:** `sent_urls` holds `(user_id, url_hash)` for every link sent; the hash ignores the fragment, `utm_` parameters, a trailing slash and the case of scheme and host. `sent_stories` holds `(user_id, story)`, so one event reported by several sources makes one item; a story may return after 48 hours, when there is a new development in it.
- **Feedback** goes to `digest_signals`, and only the digest's owner counts:
  - **Reactions** to a digest (`reaction_added` → `slack:reaction` → `news-reaction`) count +1, -1 or 0 by emoji, for every item's interest.
  - **Replies** in a digest's thread are chat turns that also count +1; the turn starts a new session that is given the digest's text, so the agent can discuss the items.
  - In `post`, each net like over the last 30 days moves an interest's items up half a point, up to a point either way.
- **Slack setup:** nothing new; `reaction_added` and `reactions:read` were already in the manifest.

## Job assistant (web channel)
The job assistant is the first feature of the web channel, Roamind's web app, a separate repo on Firebase: users sign in with Google, upload a resume and give roles, regions and notes. The user's resume lives in their memory, as their own Person note: an upload is written there, and a Slack user's chats may have put it there already. The worker finds postings, and takes the links users send in the web app or in Slack; it writes a cover letter and answer sheet for each from that note, and delivers them where the request came from: to Firestore, for review in the web app, or to the Slack thread. A Slack user needn't use the web app, and a user may use both. The user applies on their own. Nothing is submitted or filled in on any site. The data contract, with the security rules, is [web.md](web.md).

```
job-search (08, 12, 16, 20:00) ─ Firestore profiles ─▶ users (channel web)
   └ every unpaused user ─▶ job-hunt: check-skip → resume, find → pick → spawn ─▶ job-application × n
web app listener (worker) ─ Firestore snapshots
   ├ a new resume upload ─▶ resume-import: memory → fetch → update ─▶ the user's self Person note
   └ queued links ─▶ claim ─▶ job-links (uid): resume → pick → spawn ────────────▶ job-application × n
Slack turn ─ submit_job_links ─▶ after the turn: links in the message ─▶ job-links (thread) ┘
                                     check-skip → tailor → publish ─▶ Firestore applications (+ links done), or the Slack thread
```

- **Users:** every profile with an email becomes a `users` row keyed by the email's hash, with `channel = 'web'` unless the person is already a Slack user; both `job-search` and the listener do this. Web users get the web app's features, not Slack's: `Store.Users`, which feeds news shorts and user-setup, returns Slack users only, since those need a Slack identity. A web user who joins Slack becomes a Slack user.
- **The resume is the user's own Person note** (`relationship self`, in `people/`), read from disk with `notes.Self`. Ranking and tailoring get its text. `job-links` and `job-application` need only memory, so a deployment without Firebase serves links from Slack; job hunts, resume uploads and the listener need Firebase too.
- **The web app listener** (`worker/workflows/web_listen.go`) runs in the worker process, beside the Hatchet worker. It holds two Firestore snapshot listeners, outbound like every other connection, and starts runs as top-level runs. A listener that fails listens again after 30 seconds; its first snapshot holds everything there already, so nothing added meanwhile is missed.
  - **Profiles:** for a resume upload whose Storage generation (`resume_generation`) isn't `resume_import.generation`, it writes `resume_import = {queued, generation}` and starts `resume-import`. Writing first keeps the snapshots that follow from starting it again; a failed import keeps its generation, so it waits for a new upload. The profile is the only record: there is no table for imports.
  - **Links:** a collection-group query on `links` with `status == queued` (it needs the single-field index exemption in web.md). It claims a user's new links in a transaction, 20 at a time, and starts `job-links` for them. Links whose run can't start stay `processing`: queued again at once, the listener would retry them in a loop. `job-search` queues again links left `processing` for over an hour.
- **The `resume-import` DAG** (`worker/workflows/resume_import.go`), serialized per user:
  1. **`memory`** says `running` to the web app and sets up the user's Basic Memory project if they have none, as user-setup does (a web-only user's first upload).
  2. **`fetch`** saves that generation of the PDF in the user's workspace as `uploads/resume-<generation>.pdf`, where files shared in Slack go too, once per upload.
  3. **`update`** is the chat turn the user would have sharing the resume in Slack: "Remember about me from my resume.", with the attachment note chat turns get, the workspace tools and the user's memory, and `Channel: web`, so the system prompt records `web` as the evidence's source. There is no prompt of its own: the system prompt says to save every detail of a shared CV that the schema has a field for, and the person schema says how a resume updates the note (career fields replaced, contacts and locations added to, everything else kept). Read handles scans too. The task then checks that the self note exists, marks the import `done`, and deletes the upload with a generation precondition, so an upload that replaced it meanwhile stays to be imported; memory holds the resume, and the workspace a copy. Retried once.
  - When a task fails for good, the web app is told (`resume_import.status = failed`), and the upload stays until the user replaces it.
- **Schedule:** the `job-search` cron runs at fixed hours, `JobSearchHours` (08:00, 12:00, 16:00 and 20:00 in `ROAMIND_TZ`; Hatchet crons are UTC, registered at the zone's offset when the worker starts, so after a daylight-saving change the runs are an hour off until the next start; a run triggered by hand always runs). Each run brings in the profiles, starts a `job-hunt` child run for every unpaused user, and queues again links left `processing` for over an hour. There are no per-user times: a hunt skips itself while the user's picks wait for review, so a user who reviews often is searched up to four times a day, and Claude calls stay at about two per user per search plus one per application.
- **The `job-hunt` DAG** (`worker/workflows/job_hunt.go`), serialized per user, with the skip pattern of `learning-exercise`:
  1. **`check-skip`** needs a profile whose email is the user's, not paused, with roles and regions, and a resume in memory. It then closes the user's open postings that they marked `applied` or `dismissed` in the web app, or whose site now answers 404, and marks failed a picked posting whose application never came within 12 hours. **While any posting is still open, the hunt is skipped**: reviews gate new applications. It writes `search.status` for the web app.
  2. **`resume`** reads the user's own Person note.
  3. **`find`** is a `ToolsWeb` turn (`agent/prompts/find_jobs.md`) that searches Greenhouse, Lever, Ashby and Workday boards, those common in Europe and Hong Kong (SmartRecruiters, Recruitee, Personio, Workable, Teamtailor, Oracle) and career pages for each role and region, and returns up to 15 posting URLs. It runs alongside `resume`, and is retried once.
  4. **`pick`** merges the postings by canonical URL (`jobs.Parse`), drops those found for the user before (`job_postings`), and reads up to 20 from their sites (`connectors/jobs`, public addresses only): the public APIs of Greenhouse (with the form's questions, also for a company's own careers page with a `gh_jid`), Lever, Ashby (the board is decoded as a stream, up to 64 MB) and Workday, and of SmartRecruiters, Recruitee (with the form's questions), Personio (its XML feed), Workable and Oracle Recruiting Cloud, common in Europe and Hong Kong. Any other page is read from the schema.org `JobPosting` it carries for search engines (Teamtailor, SuccessFactors, many job boards) when its HTML has one; else it is rendered in headless Chrome and read as text, falling back to the HTML as sent if rendering fails. Rendering uses `jobs.Chrome`, two pages at a time, 45 s each: a fresh profile per page, no images, WebRTC kept to the proxy, and all traffic through `safehttp.Proxy`, which connects only to public addresses, so a page's scripts can't reach Hatchet, the search models or the tailnet. The rendered HTML is read as text like any page; no turn ever gets a browser. One `ToolsNone` turn (`agent/prompts/rank_jobs.md`) scores each posting's fit 0-10 against the resume, roles, regions and notes, with reasons. The best `ROAMIND_JOBS_PER_RUN` (default 5) with a fit of at least 6 are recorded as `picked` before anything else happens; those below 6 are recorded as `passed`, so they are never judged again. No retries.
  5. **`spawn`** starts a `job-application` child run per pick; a pick whose run can't start is marked failed.
  - A failed task tells the web app the search failed (on-failure task).
- **The `job-application` DAG** (`worker/workflows/job_application.go`), serialized per user:
  1. **`check-skip`** skips a posting that is no longer `picked`.
  2. **`tailor`** is a `ToolsNone` turn (`agent/prompts/tailor_application.md`) that writes `cover_letter_md` and `answers` from the resume note and the user's notes: one per form question when the site publishes them (Greenhouse, Recruitee), else the common ones. Answers the resume can't give are left for the user to fill in. Retried once.
  3. **`publish`** creates the application in Firestore under the posting's URL hash (never overwriting, so a retry can't undo a review), then marks the posting `published`.
  - When a task fails for good, the posting is marked `failed`, so it doesn't hold up the user's next hunt.
- **The `job-links` DAG** (`worker/workflows/job_links.go`) drafts the postings a user linked to: in the web app (`profiles/{uid}/links`, started by the listener with the uid) or in Slack (started after the chat turn with the thread). Serialized per user. It has no `check-skip`: whenever a user has links, it runs, without needing roles or regions, an unpaused search or reviewed applications, since the user asked for these postings.
  1. **`resume`** is job-hunt's. Without a resume in memory it fails, and the links fail with a reason asking for one.
  2. **`pick`** resolves each link to its canonical URL (`jobs.Parse`), so links to the same posting are drafted once, and fails those that aren't web addresses or are on a blocked domain. For the web app, a posting already found for the user keeps its application: picked, published or closed, its links are marked `done` with that application's id; passed and failed ones are drafted again (`RepickPosting`). It reads the rest from their sites, failing those it can't read, and one `ToolsNone` turn (`rank_jobs.md`) scores their fit: against the profile's roles, regions and notes for the web app, against the resume alone for Slack. For the web app every one is recorded as `picked`, whatever its fit. No retries.
  3. **`spawn`** starts a `job-application` run per posting, carrying its link ids.
  - **Results go back where the links came from** (`jobReply`, `worker/workflows/job_reply.go`). For the web app, `job-application`'s `publish` saves the application, sets its `link_id` and marks its links `done`; failed links are marked `failed`, with the reason, and a failed run fails the links still processing. For Slack, each result is a message in the thread: the application (cover letter and answers, in mrkdwn), or the links that failed and why.
  - For the web app, a link's posting is a `job_postings` row like any other: while its application is open, the daily hunt waits for its review. Slack has no review, so links from Slack record nothing: they are drafted every time they are sent, and never hold up a hunt.
- **Links from Slack:** chat turns get the `jobs` tool set when memory is set up, the `submit_job_links` tool (`mcp/tools/jobs.go`). The tool only records the links in SQLite (`job_link_requests`), so `bin/mcp` holds no Hatchet or cloud credentials. After the turn, the chat-turn harness takes the requests and starts `job-links` with the turn's thread, for only those whose canonical URL is in the user's own message, so a page the turn read can't make it apply anywhere. No web app sign-in is needed.
- **`job_postings` statuses:** `picked` and `published` are open; `closed` (reviewed, or gone from its site), `failed` and `passed` are not. Every posting found for a user stays, so none is shown twice.
- **Blocked domains:** `ROAMIND_JOBS_BLOCKED_DOMAINS` lists domains, with their subdomains, that the job search never reads: `pick` drops their postings before anything fetches or records them, the posting reader refuses them (`jobs.ErrBlocked`), and the `find` turn's WebFetch denies them.
- **Setup:** memory, for links from Slack; for the web app, also `FIREBASE_PROJECT_ID`, `FIREBASE_STORAGE_BUCKET` and `GOOGLE_APPLICATION_CREDENTIALS` (a service account key). Without Firebase, job hunts and resume imports aren't registered and nothing listens.
