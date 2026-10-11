# Roamind

A self-hosted, multi-user personal agent in Slack, organised around your goals.

## What sets it apart

- **Each goal gets its own channel.** Roamind recognises when you set out to build, learn or achieve something. It records that as a goal and opens a private Slack channel for it. Every conversation there is about that goal, with no @mention needed: Roamind loads the goal's tasks and progress first and files new updates under it. Your goals become a set of focused workspaces rather than one long chat.
- **Daily exercises for what you're learning.** Every morning at 9:00, each learning goal's channel gets one exercise. Roamind evaluates your answer in its thread, and the results of recent exercises set the topic and difficulty of the next one. If you skip an exercise, no new ones are posted until you answer it.
- **Memory is a typed knowledge graph you own.** Facts are saved as plain Markdown notes that follow schemas: Person, Place, Event, Preference, Goal, Progress, Task and Exercise.
  - The notes link to each other through typed relations, such as works at, task of and progress of.
  - Every fact records its provenance: whether it was stated, inferred, observed or corrected, and on which channel and date.
  - You can read, edit and version the notes like any other files.
- **Isolation per user, not just per row.** Each user has their own memory database, their own Basic Memory configuration and their own workspace on disk. Shell commands run in a bubblewrap sandbox that can't see other users, the server's secrets or the home directory. Users are identified by a hash of their email; everyone in the Slack workspace can use the agent, people from other organisations in shared channels can't.
- **Claude Code is the agent.** The agent loop is `claude -p`, so the agent gets Claude Code's full toolset: web search and fetch, file editing, reading PDFs and images, and shell commands in a persistent per-user workspace. Roamind contributes the orchestration, memory, identity and Slack integration.
- **Durable, observable runs.** Every message is a [Hatchet](https://hatchet.run) run, serialized per thread. Background work, such as creating goal channels, runs as separate retried tasks. You can inspect, trace and replay every run in the Hatchet dashboard.
- **Local semantic search.** Memory search is hybrid. Qwen3 embedding and re-ranking models are served by vLLM on your own GPU, so the memory index is built and searched on your machine.
- **A minimal public surface.** The only public endpoint is the Slack webhook path, exposed through Tailscale Funnel. Dashboards are reachable only over your tailnet, including from your phone, and only for listed logins.

## How it works

Slack sends events to Hatchet's Slack webhook. Each message becomes one `chat-turn` run, which drives `claude -p` and streams the reply into the Slack thread by editing a message. Claude talks to Basic Memory's own MCP server, pinned to the user's project. After each turn, the harness checks whether any active goal still needs a channel.

```
Slack ─HTTPS─▶ Tailscale Funnel (webhook path only) ─▶ Hatchet ─event slack:chat─▶ worker: chat-turn
                                                                                         ├ identity (sha256(email))
                                                                                         ├ thread → claude session (SQLite)
                                                                                         └ claude -p --resume … → throttled chat.update
                                                                                              └ stdio: bm mcp --project bm-u-<name>-<n>
                                                                                         └ after the turn: an active goal without a channel? ─▶ goal-channels run
                                                                                              └ conversations.create (private #goal-<slug>) → invite owner → intro
```

## Modules (`go.work`)

| Module | What it holds |
|---|---|
| `agent` | `claude -p` runner and stream-json decoder; packet translation; Go port of onyx's citation processor; `cmd/turn` dev CLI. `agent/protocol` is the contract with the channels: packet types (onyx-style), turn input/output and the snapshot accumulator. It's a separate package with no dependencies, so channels import it without the agent runtime |
| `channels` | SQLite store (users, threads, goal channels, exercises, inbound events), email-hash identity, Slack event types, mrkdwn renderer, streaming updater |
| `worker` | The Hatchet worker, laid out like Hatchet's Go quickstart: `cmd/worker`, `cmd/setup-webhook`, `client` (the Hatchet client) and `workflows`, one file per workflow with its tasks and wiring: `chat-turn`, `goal-channels`, `learning-exercises`, `learning-exercise` |
| `memory` | Per-user Basic Memory projects: creates and seeds a project, builds claude's `--mcp-config` and tool allow/deny lists, reads goal notes from disk |

**Design rules**
- Conversation history lives only in claude's session files (`~/.claude/projects/<cwd>`). SQLite maps each thread to its session id and stores nothing else about the conversation.
- Claude always runs in `data/users/<user_id>/workspace`, so `--resume` can find the session.
- **Each user has a sandboxed workspace.** Claude gets web search and fetch, the file tools and Bash, working in `data/users/<user_id>/workspace`. That directory persists across conversations and is private to the user. See `agent/workspace.go`:
  - **File tools:** `permissions.blockReadsOutsideWorkingDirectories` stops reads outside the workspace. Edits are allowed only by an `Edit(//<workspace>/**)` rule, and in `dontAsk` mode anything not allowed is denied, so a bare `Write`/`Edit` rule would have allowed any path.
  - **Bash** runs in Claude Code's bubblewrap sandbox (`bwrap` and `socat` must be installed). It can write only to the workspace and `$TMPDIR`. It can't read the home directory, which covers other users' workspaces and memory, `data/`, `.env` and `~/.claude`. `allowUnsandboxedCommands: false` and `failIfUnavailable: true` mean nothing ever runs unsandboxed.
  - **Network:** `WebFetch(domain:*)` opens the web to WebFetch and to sandboxed commands. The sandbox proxy still refuses local addresses, so Hatchet's ports are unreachable from the sandbox.
  - **Files from Slack:** files shared with a DM (`subtype: file_share`) or a mention are downloaded with the bot token (scope `files:read`, up to 50 MB each) into `uploads/` in the workspace. The message the agent sees ends with a list of them, and Claude's Read tool reads PDFs, images and text directly.
  - User settings and ambient MCP servers are still ignored (`--strict-mcp-config`, `--setting-sources ""`); the turn's own `--settings` carries the rules above.
- A thread belongs to the user who started it. Other users in that thread are asked to start their own.
- Runs in the same thread are serialized by the Hatchet concurrency key `channel:thread_ts`.
- Runs started from webhook events can't carry custom run metadata. To find a conversation's runs, use the run output (`thread`, `user_id`, `session_id`).

**Memory**
- Each user has one Basic Memory project, `bm-u-<slugified display name>-<n>`. The `-<n>` keeps names unique.
  - The name is assigned when the user is created and stored in `users.memory_project`.
  - It's read back with the user row, so nothing is computed per turn, and it doesn't change if the user is renamed.
- Every turn starts `bm mcp --project <that project>` over stdio through `--mcp-config`. As a second layer, bm enforces the pinning itself: it ignores `project` arguments and `search_all_projects`, and it refuses to create or delete projects.
- The agent has full read/write access to its project: every bm tool is allowed (`--allowedTools mcp__memory`), including delete and move. Only project and workspace management (`list_memory_projects`, `create_memory_project`, `delete_project`, `list_workspaces`) is passed to `--disallowedTools`, since `--project` refuses those anyway.
- **Search models on the GPU:** Basic Memory's hybrid search uses Qwen3-Embedding-0.6B (1024-dim) and re-ranks with Qwen3-Reranker-0.6B, both served by vLLM on the local GPU (`embed` on 127.0.0.1:8800 and `rerank` on 127.0.0.1:8801 in process-compose; `deploy/bin/run-vllm.sh`).
  - Each takes 35% of an 8 GB card, with a 4096-token context.
  - bm reaches them through its `litellm` provider, configured by `memory.DefaultSearch`, which the harness writes into every user's `config.json` on each worker start.
  - Search queries get a Qwen3 instruction prefix, and the re-ranker's default instruction is set for our notes in `deploy/vllm/qwen3_reranker.jinja`.
  - Changing the embedding model makes bm re-embed existing notes.
- **Each user has their own Basic Memory setup and database.** Under `data/memory/`, separate from any personal `~/.basic-memory`:
  - `fastembed_cache/` holds the embedding model, downloaded once and shared read-only;
  - `users/<project>/config/` holds that user's `config.json` (listing only their project) and `memory.db` (index, full-text search and embeddings);
  - `users/<project>/notes/` holds their Markdown notes.

  Each turn's bm gets only that user's `BASIC_MEMORY_CONFIG_DIR`, so it can't open another user's database. To delete a user's memory, remove `users/<project>`. Notes are plain Markdown, so you can browse and edit them directly.
- New projects are seeded with the Picoschema notes in `memory/schemas/*.md` (person, place, preference, event, goal, progress, task, exercise), which are embedded in the binary. Add or edit a file there to change the schemas; existing projects keep their copies. The prompt tells the model which folder and `note_type` to use for each.
- **Preferences are atomic:** one note per preference in `preferences/<subject>/`, with exactly the schema fields `[preference]`, `[subject]` and `[evidence]`.
  - `subject` is an enum: food, travel, fitness, health, work, tech, communication, shopping, entertainment, home, finance, other.
  - `evidence` is an enum: stated, inferred, observed or corrected. The channel and date go in its context, e.g. `- [evidence] stated (slack, 2026-10-11)`.
- **What `schema_validate` checks:** that required fields are present and enum values are allowed, with warnings rather than errors. It doesn't check types, formats (dates, coordinates), cardinality or the target type of a relation; the prompt carries those rules.
- **Required fields:** a person's `relationship` (`self` for the user's own note, exactly one per project), a place's `kind`, an event's `date` (as precise as known), a goal's `status` and `outcome`, and a task's `task_of` and `status`.
- **Person** is kept small: `relationship`, `contact`, `works_at` (a link to a Place), and the career fields `experience`, `education` and `interest`.
  - The user's own resume goes on their `self` note.
  - Each role or degree is one line in a fixed format, so its parts stay together, e.g. `- [experience] Postdoctoral researcher, ETH Zurich, 2021-09 to present: highlights (stated, slack, 2026-10-11)`.
  - The current job ends with `to present`.
  - These formats are enforced only by the prompt.
- **Goals, progress and tasks:** a Goal note (`goals/`) is anything worked on over time: a project being built (`kind build`), something being learned (`kind learn`) or another aim (`kind personal`).
  - It holds only what defines the goal: `status`, `outcome`, `kind`, `target_date`, `why`, `link`, `with` and, for learning goals, `exercise_preference`.
  - **Progress:** each update is its own note in `progress/<goal>/` (`date`, `summary`, `next`, `blocker`), linked back with `progress_of`.
  - **Tasks:** optional. Each task is a note in `tasks/<goal>/` (`status`, `due`, `with`) that must belong to exactly one goal via `task_of`; the agent asks which goal when none fits.
  - Both link to the goal, so a goal accumulates updates and tasks without its own note growing.
- **Links between types:** people link to workplaces with `works_at`. Events link to people and places with `with` and `at`, and goals link to people with `with`.
- **Other notes** (people, places, events) are one note per thing. Each fact ends with its evidence as Basic Memory observation context, e.g. `- [interest] oscillatory neural networks (stated, slack, 2026-10-11)`. bm stores that context in the observation's `context` field.
- **Links between notes** are relations written as `- works_at [[places/ibm-research-zurich]]`, using the target's permalink so the link is exact.
  - bm indexes each one as a row in its `relation` table, with `to_id` as a foreign key to the target note. A link to a note that doesn't exist yet stays unresolved (`to_id` NULL) until that note is created.
  - People link to Place notes with `works_at`. Places hold `location` (the address) and `coordinates` (`lat,lon`, WGS84 decimal degrees, saved only when the user gives them).
  - Schema notes are indexed like any other note, so their text must not contain fact lines (`- …`) or `[[…]]`. Those would become facts and links in every user's memory. `TestSchemasHaveNoFactsOrLinks` enforces this.
  - The schema header must be valid YAML; bm silently ignores a schema it can't parse, so quote values that contain `: `. `TestSchemaFrontmatterParses` enforces this.
- **Evidence** says how a fact was learned (`stated`, `inferred`, `observed` or `corrected`), plus the channel and date.
- The agent decides what to save; the rules are in `agent/prompts/system.md`.

**Goal channels**
- **What gets a channel:** every Goal note (`goals/`) whose `status` is `active` gets a private Slack channel, `#goal-<slugified title>`. A goal saved without a status counts as active.
  - If the name is taken, the channel gets a suffix: `-2`, `-3`, …
  - A paused goal gets its channel when it becomes active again.
  - Achieved and dropped goals get none.
- **When it's created:** after every chat turn, `goalchannels.Trigger` compares the user's goal notes on disk (`memory.Goals`) with the `goal_channels` table. This needs only file reads and SQLite, so most turns start nothing.
  - When an active goal has no channel, it starts a `goal-channels` run (`worker/workflows/goal_channels.go`).
  - That run creates the channel, records it, invites the owner and posts an intro.
  - Runs are serialized per user and retried up to 3 times. The row is saved right after the channel is created, so a retry never creates a second channel.
  - A goal that already exists gets its channel on the user's next message anywhere.
- **The `goal_channels` table:** it maps (user, goal permalink, provider) to a channel id and is managed only by the harness. The agent is never told about channels.
- **Shared goal channels:** several users can share one channel, each linked to the goal in their own memory. Each user has their own row, with a unique key on (provider, channel, user). In the channel, each user's messages run as their own turns, with their own memory and their own goal note, so memory stays isolated per user. To share a channel, point the other user's row for their goal at it; they can then archive their own channel.
- **Chatting in a goal channel:** plain messages from any user with a goal in the channel start turns, with no @mention needed.
  - Slack also sends an `app_mention` copy of a message that mentions the bot; that copy is skipped, so each message runs once.
  - The turn's `protocol.GoalRef` adds a paragraph to the system prompt naming the goal and its permalink. That paragraph asks the agent to load the goal with `build_context` and to link new progress and tasks to it.
  - As everywhere, each top-level message starts its own thread and claude session.
- **Other private-channel messages:** the webhook routes every `message` event from a private channel (`channel_type == group`) to `slack:chat`. `chatturn.route` then skips the ones that aren't the owner's messages in their own goal channel. Other people in a goal channel, and anyone in other private channels, reach the agent only by @mentioning it, and those turns carry no goal.
- **Archiving:** the harness never archives or deletes channels; users archive them. An archived goal's row stays, so its channel isn't re-created.
- **Slack scopes:** `groups:write` (create private channels and invite people), plus `groups:history` and the `message.groups` event for reading them.

**Daily exercises (learning goals)**
- **What gets one:** every active goal with `kind learn` that has a channel, at 9:00 in `ROAMIND_TZ` (default: the server's zone).
- **Scheduling:** Hatchet crons run in UTC, so `ExerciseCrons` registers the UTC times of 9:00 local in winter and in summer, for example `0 8 * * *` and `0 7 * * *` for Europe/Berlin. The `learning-exercises` cron task does nothing unless it is 9:00 local. Otherwise it starts one `learning-exercise` run per learning goal, as child runs; a DAG can't fan out to a varying number of goals.
- **The `learning-exercise` DAG** (`worker/workflows/learning_exercise.go`), serialized per goal:
  1. **`check-skip`** returns a reason to skip when the goal isn't an active learning goal with a channel, today's exercise is already posted, or the latest exercise is unanswered. The latest exercise blocks every later day until the user replies in its thread; answering late resumes exercises the next morning. Hatchet still runs the children of a skipped task, so every later task lists `check-skip` as a parent and skips itself on that reason.
  2. **`build-context`** reads the goal note, with its `exercise_preference` lines, and the latest five exercise notes, with the user's answers and results. It reads them from disk, because bm's `build_context` lists the linked notes but not their content.
  3. **`draft-exercise`** runs an agent turn with that context (`agent/prompts/draft_exercise.md`). Its structured output (`claude -p --json-schema`) is topic, difficulty, format and the exercise text. The turn gets no memory tools; it keeps web search for material. It is retried once.
  4. **`post`** posts the exercise as a new top-level message in the goal channel, with overflow in its thread. It then records the thread → drafting session and the `exercises` row, and writes the Exercise note at `exercises/<goal>/<date>` with `bm tool write-note`. It has no retries, so an exercise is never posted twice.
- **Answering** is not part of the workflow. When the goal's owner replies in an exercise thread, chat-turn sets `answered_at` and resumes the drafting session, so the agent knows exactly what was asked. The system prompt names the exercise note; the agent evaluates the answer, appends it to the note under Answer, and adds `result`, `score`, `feedback` and `next`.
- **Exercise preferences** are `exercise_preference` lines on the goal note, saved by the agent when the user states them in the goal channel.

## Deployment

```
hatchet server start (Hatchet CLI)        process-compose (`process-compose up`)
  postgres + hatchet-lite                    hatchet-ready → waits for /api/ready
  dashboard/API :8889, gRPC :7078            caddy  → tailnet dashboards (hatchet.<tailnet>.ts.net)
  profile "local" (token, addresses)         funnel → public Slack webhook path only
                                             embed/rerank → vLLM search models (GPU, loopback)
                                             worker → chat-turn, goal-channels, learning-exercise(s)
```

- **Hatchet is started by the Hatchet CLI** (`./build.sh hatchet`, i.e. `hatchet server start`, pinned to the SDK's version), outside process-compose.
  - It creates the `local` profile. `deploy/bin/with-hatchet.sh` loads that profile (`hatchet profile env`) for the worker, Caddy and the `webhook`/`funnel` commands, so no token goes in `.env`.
  - The ports come from the profile, so the auto-picked ports (8889/7078 here) are fine.
- **Dashboards** are served by Caddy built with [caddy-tailscale](https://github.com/tailscale/caddy-tailscale).
  - Each dashboard joins the tailnet as its own device, e.g. `hatchet`, and so gets its own name and certificate (`hatchet.<tailnet>.ts.net`). It isn't a subdomain of this machine.
  - They're reachable anywhere you have the Tailscale app, including your phone.
  - Only the Tailscale logins in `ROAMIND_DASHBOARD_USERS` get in, since the tailnet may be shared.
  - Caddy strips `Domain=localhost` from Hatchet's login cookie, so you can log in under the tailnet name.
  - To add a dashboard, copy the hatchet site block in `deploy/caddy/Caddyfile` with a new device name.
  - Path-based routing doesn't work for Hatchet: its page uses `<base href="/">` and calls `/api` from the site root.
- **The only public endpoint** is Hatchet's webhook path, exposed through Tailscale Funnel.
- **Caution:** `hatchet server start` binds its ports on all interfaces (0.0.0.0), so tailnet and LAN peers can reach `:8889` directly, bypassing Caddy. Change the default admin password, and firewall those ports, e.g. `sudo ufw deny in on tailscale0 to any port 8889,7078 proto tcp`.

## Setup

All one-shot steps are subcommands of `./build.sh` (run `./build.sh help` to list them). process-compose only runs the long-lived processes (caddy, embed, rerank, worker).

1. **Config.** Copy `.env.example` to `.env`.
   - Set `TS_AUTHKEY` (needed only for Caddy's first run) and `ROAMIND_DASHBOARD_USERS` (comma-separated logins, e.g. `you@example.com`).
2. **Slack app.** Create it from `deploy/slack-app-manifest.yaml` and install it. Put `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` in `.env`.
   - **Upgrading an app created from an older manifest:**
     - Add the bot scope `groups:write` under OAuth & Permissions, then reinstall the app.
     - Re-run `./build.sh webhook` so the routing expression includes private-channel messages.
     - Rebuild and restart the worker.
3. **Hatchet environment.** `./build.sh hatchet-env` does three things:
   - starts the Hatchet server;
   - registers the Slack webhook, with the routing expression `channels/slack.EventKeyExpression`;
   - opens Tailscale Funnel on the webhook path only. The tailnet policy must allow Funnel for this node.

   It prints the Slack Request URL; paste it into the Slack app under Event Subscriptions.
4. **Build.** `./build.sh all` builds `bin/caddy` and `bin/worker`. `./build.sh vllm` installs vLLM (pinned, on a uv-managed Python so Triton finds `Python.h`) and downloads the two search models. This needs an NVIDIA GPU. For memory, install Basic Memory's `bm` CLI on `PATH` with `uv tool install basic-memory` (tested with 0.23.2). Its first start downloads an embedding model.
5. **Run.** `process-compose up` from the repo root. It reads `process-compose.yaml` and `.env`. Then log into `https://hatchet.<tailnet>.ts.net` as `admin@example.com` / `Admin123!!` and change the password.

   To start at boot instead, `./build.sh service` installs and enables a `roamind` systemd user service that runs `process-compose up --tui=false` from the repo root. Manage it with `systemctl --user start|stop|status roamind`, and attach with `process-compose attach`. It starts at boot without a login only when lingering is on (`sudo loginctl enable-linger $USER`).

Development commands:
- `./build.sh test` and `./build.sh vet` run in every module.
- `./build.sh bm [project] <args>` runs Basic Memory's CLI against a user's project (`./build.sh bm project info`, `./build.sh bm tool search-notes …`). It sets `BASIC_MEMORY_CONFIG_DIR` for that one command only, so your own `bm` and MCP setups keep using `~/.basic-memory`.
- `./build.sh turn "hello"` runs one real `claude -p` turn from the terminal.
