# Roamind

A multi-user personal agent. Phase 1 is chat over Slack. Slack sends events to Hatchet's Slack webhook. Each message becomes one `chat-turn` run, which drives `claude -p` and streams the reply into the Slack thread by editing a message. Phase 2 adds long-term memory: each user has a [Basic Memory](https://github.com/basicmachines-co/basic-memory) project, and claude talks to Basic Memory's own MCP server directly.

```
Slack ─HTTPS─▶ Tailscale Funnel (webhook path only) ─▶ Hatchet ─event slack:chat─▶ worker: chat-turn
                                                                                         ├ identity (sha256(email)), allowlist
                                                                                         ├ thread → claude session (SQLite)
                                                                                         └ claude -p --resume … → throttled chat.update
                                                                                              └ stdio: bm mcp --project bm-u-<name>-<n>
```

## Modules (`go.work`)

| Module | What it holds |
|---|---|
| `protocol` | Packet types (onyx-style), turn input/output, snapshot accumulator |
| `agent` | `claude -p` runner and stream-json decoder; packet translation; Go port of onyx's citation processor; `cmd/turn` dev CLI |
| `channels` | SQLite store (users, threads, inbound events), email-hash identity and allowlist, Slack event types, mrkdwn renderer, streaming updater |
| `workflows` | Hatchet worker (`cmd/worker`), `chat-turn` task, `cmd/setup-webhook` |
| `memory` | Per-user Basic Memory projects: creates and seeds a project, builds claude's `--mcp-config` and tool allow/deny lists |

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
- New projects are seeded with the Picoschema notes in `memory/schemas/*.md` (person, place, project, preference, event), which are embedded in the binary. Add or edit a file there to change the schemas; existing projects keep their copies. The prompt tells the model which folder and `note_type` to use for each.
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
  - It holds only what defines the goal: `status`, `outcome`, `kind`, `target_date`, `why`, `link` and `with`.
  - **Progress:** each update is its own note in `progress/<goal>/` (`date`, `summary`, `next`, `blocker`), linked back with `progress_of`.
  - **Tasks:** optional. Each task is a note in `tasks/<goal>/` (`status`, `due`, `with`) that must belong to exactly one goal via `task_of`; the agent asks which goal when none fits.
  - Both link to the goal, so a goal accumulates updates and tasks without its own note growing.
- **Links between types:** people link to workplaces with `works_at`. Events link to people and places with `with` and `at`, and goals link to people with `with`.
- **Other notes** (people, places, projects, events) are one note per thing. Each fact ends with its evidence as Basic Memory observation context, e.g. `- [interest] oscillatory neural networks (stated, slack, 2026-10-11)`. bm stores that context in the observation's `context` field.
- **Links between notes** are relations written as `- works_at [[places/ibm-research-zurich]]`, using the target's permalink so the link is exact.
  - bm indexes each one as a row in its `relation` table, with `to_id` as a foreign key to the target note. A link to a note that doesn't exist yet stays unresolved (`to_id` NULL) until that note is created.
  - People link to Place notes with `works_at`. Places hold `location` (the address) and `coordinates` (`lat,lon`, WGS84 decimal degrees, saved only when the user gives them).
  - Schema notes are indexed like any other note, so their text must not contain fact lines (`- …`) or `[[…]]`. Those would become facts and links in every user's memory. `TestSchemasHaveNoFactsOrLinks` enforces this.
  - The schema header must be valid YAML; bm silently ignores a schema it can't parse, so quote values that contain `: `. `TestSchemaFrontmatterParses` enforces this.
- **Evidence** says how a fact was learned (`stated`, `inferred`, `observed` or `corrected`), plus the channel and date.
- The agent decides what to save; the rules are in `agent/prompts/system.md`.

## Deployment

```
hatchet server start (Hatchet CLI)        process-compose (`process-compose up`)
  postgres + hatchet-lite                    hatchet-ready → waits for /api/ready
  dashboard/API :8889, gRPC :7078            caddy  → tailnet dashboards (hatchet.<tailnet>.ts.net)
  profile "local" (token, addresses)         funnel → public Slack webhook path only
                                             embed/rerank → vLLM search models (GPU, loopback)
                                             worker → chat-turn
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
   - Set `ROAMIND_ALLOWED_EMAILS`.
2. **Slack app.** Create it from `deploy/slack-app-manifest.yaml` and install it. Put `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` in `.env`.
3. **Hatchet environment.** `./build.sh hatchet-env` does three things:
   - starts the Hatchet server;
   - registers the Slack webhook, with the routing expression `channels/slack.EventKeyExpression`;
   - opens Tailscale Funnel on the webhook path only. The tailnet policy must allow Funnel for this node.

   It prints the Slack Request URL; paste it into the Slack app under Event Subscriptions.
4. **Build.** `./build.sh all` builds `bin/caddy` and `bin/worker`. `./build.sh vllm` installs vLLM (pinned, on a uv-managed Python so Triton finds `Python.h`) and downloads the two search models. This needs an NVIDIA GPU. For memory, install Basic Memory's `bm` CLI on `PATH` with `uv tool install basic-memory` (tested with 0.23.2). Its first start downloads an embedding model.
5. **Run.** `process-compose up` from the repo root. It reads `process-compose.yaml` and `.env`. Then log into `https://hatchet.<tailnet>.ts.net` as `admin@example.com` / `Admin123!!` and change the password.

Development commands:
- `./build.sh test` and `./build.sh vet` run in every module.
- `./build.sh bm [project] <args>` runs Basic Memory's CLI against a user's project (`./build.sh bm project info`, `./build.sh bm tool search-notes …`). It sets `BASIC_MEMORY_CONFIG_DIR` for that one command only, so your own `bm` and MCP setups keep using `~/.basic-memory`.
- `./build.sh turn "hello"` runs one real `claude -p` turn from the terminal.

## Step-0 spike results (2026-10-10, hatchet v0.110.22)

**Verified locally against hatchet-lite:**
- The Slack `url_verification` challenge is answered by Hatchet.
- A bad `X-Slack-Signature` is rejected with 403.
- The raw Slack JSON body arrives as the task input (`slack.Envelope`).
- The event-key expression sends bot and other non-chat events to `slack:ignored:*`, so they create no run.
- An `app_mention` produces exactly one `chat-turn` run.
- `/api/ready` works as the readiness probe.
- The webhook can be created from code (`features.WebhooksClient`, source `SLACK`, HMAC on `X-Slack-Signature`).

**Not yet verified:**
- A real Slack app end to end (Slack → Funnel → Hatchet → worker → Slack).
- Caddy and caddy-tailscale joining the tailnet. The config validates, and handlers run in order: tailnet auth, then 403 for anyone not listed, then the proxy.
- Hatchet embedded mode. Not used, because its dashboard needs a per-start URL token.

**Also verified:**
- The webhook and its signing secret survive recreating the hatchet-lite container.
- Changing `SERVER_URL` invalidates existing API tokens.
- With `hatchet server start`:
  - the worker connects using the CLI profile, and a signed Slack event reaches `slack:chat`;
  - login works through a Caddy proxy under a different host name once the cookie's Domain is stripped.

**Verified over the public internet through Tailscale Funnel:**
- The dashboard, the login page, `/api/ready` and the tenant API all return 404, so only the webhook path is exposed.
- A signed Slack URL check at `…/webhooks/slack` returns the challenge, which shows `--set-path` keeps the path prefix.
- A bad signature returns 403.

**Known limitation:** Runs started from webhook events can't carry custom run metadata. To find a conversation's runs, use the run output (`thread`, `user_id`, `session_id`) rather than metadata filters.
