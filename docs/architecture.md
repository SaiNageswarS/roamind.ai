# Architecture

How Roamind works inside. For an overview, see the [README](../README.md).

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
| `agent` | `claude -p` runner and stream-json decoder; packet translation; Go port of onyx's citation processor; `cmd/turn` dev CLI. `agent/protocol` is the contract with the Slack layer: packet types (onyx-style), turn input/output and the snapshot accumulator. It's a separate package with no dependencies, so the Slack updater imports it without the agent runtime |
| `channels` | SQLite store (tables in `store/schema/*.sql`, one file per area; users, threads, goal channels, exercises, the interest catalog and what users follow, news channels, digests, sent links and feedback, inbound events), RSS/Atom feed reader (`feeds`), email-hash identity, Slack event types, mrkdwn renderer, streaming updater |
| `worker` | The Hatchet worker, laid out like Hatchet's Go quickstart: `cmd/worker`, `cmd/setup-webhook`, `client` (the Hatchet client) and `workflows`, one file per workflow with its tasks and wiring: `chat-turn`, `goal-channels`, `learning-exercises`, `learning-exercise`, `news-channels`, `news-shorts`, `news-digest`, `news-reaction` |
| `memory` | Per-user Basic Memory projects: creates and seeds a project, builds claude's bm server entry and tool allow/deny lists, reads goal notes from disk |
| `mcp` | Roamind's MCP server, which claude starts per chat turn over stdio, pinned to the turn's user: `cmd/mcp` (`bin/mcp`) and `tools`, one file per domain (`interests.go`: following and adding news interests). Built on the official Go MCP SDK |

## Design rules
- Conversation history lives only in claude's session files (`~/.claude/projects/<cwd>`). SQLite maps each thread to its session id and stores nothing else about the conversation.
- Chat turns run in `data/users/<user_id>/workspace`, so `--resume` can find the session.
- **A turn gets the tools its input warrants** (`TurnInput.Tools`). Chat turns and exercise drafting get the workspace set below (`ToolsWorkspace`). `ToolsWeb` (WebSearch and WebFetch only, in the workspace) is available for turns that need only the web. News ranking, whose whole input is third-party feed text, gets no tools (`ToolsNone`), in a throwaway directory with no saved session.
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
  - `evidence` is an enum: stated, inferred, observed or corrected. The source and date go in its context, e.g. `- [evidence] stated (slack, 2026-10-11)`.
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
- **Evidence** says how a fact was learned (`stated`, `inferred`, `observed` or `corrected`), plus the source (`slack`) and date.
- The agent decides what to save; the rules are in `agent/prompts/system.md`.

## Goal channels
- **What gets a channel:** every Goal note (`goals/`) whose `status` is `active` gets a private Slack channel, `#goal-<slugified title>`. A goal saved without a status counts as active.
  - If the name is taken, the channel gets a suffix: `-2`, `-3`, …
  - A paused goal gets its channel when it becomes active again.
  - Achieved and dropped goals get none.
- **When it's created:** after every chat turn, `goalchannels.Trigger` compares the user's goal notes on disk (`memory.Goals`) with the `goal_channels` table. This needs only file reads and SQLite, so most turns start nothing.
  - When an active goal has no channel, it starts a `goal-channels` run (`worker/workflows/goal_channels.go`).
  - That run creates the channel, records it, invites the owner and posts an intro.
  - Runs are serialized per user and retried up to 3 times. The row is saved right after the channel is created, so a retry never creates a second channel.
  - A goal that already exists gets its channel on the user's next message anywhere.
- **The `goal_channels` table:** it maps (user, goal permalink) to a Slack channel id and is managed only by the harness. The agent is never told about channels.
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
- **The channel:** `news-channels` (`worker/workflows/news_channels.go`) creates the private `#news-shorts-<name>` channel (with a `-2` suffix if the name is taken), records it in `news_channels` first, invites the user, makes them follow the catalog's default interests and posts an intro. New users get theirs after their first chat turn (the `AfterTurn` hook); existing users get theirs from the next `news-shorts` run.
- **The interest catalog** is the `interests` table: each interest with its curated RSS/Atom feeds (`sources`) and its `rubric` in prose: two to four separately judged criteria scored 0 to 10, with what each band of scores means, when an item is not to be included at all, and optionally the categories to file items under. The curated ones are pre-built in `worker/catalog/interests.json`, with each rubric in `worker/catalog/rubrics/*.md`; `"default": true` marks the ones every user follows. The worker writes them to the store at every start, and deletes curated interests no longer in the file, with their follows; edit those files to change them. Interests the agent adds belong to the user they were added for (`owner`): each user sees the curated interests and their own, never another user's. Users follow interests by id (`user_interests`, active or muted).
- **Roamind's MCP server** is how the agent changes what a user follows. Every chat turn gets it next to bm: claude starts `bin/mcp --data <dir> --user <id>` over stdio as the `roamind` server, pinned to the turn's user the way bm is pinned to a project (`mcp/cmd/mcp`, built on the official Go MCP SDK). The worker finds `bin/mcp` next to its own binary, or at `ROAMIND_MCP_BIN`. Its interest tools (`mcp/tools/interests.go`):
  - `list_interests`: the user's catalog, curated and their own, with feeds and rubrics, and what the user follows.
  - `follow_interest`: follow an interest of their catalog, or resume a muted one.
  - `add_interest`: add an interest of the user's own, with its feeds and a prose rubric in the same style, and follow it. Every feed is fetched and must parse as RSS or Atom.
  - `set_interest_status`: mute or resume.
  In the news channel, the owner's plain messages are news turns, and their prompt lists what the user follows.
- **Scheduling:** the `news-shorts` cron runs once a day at 16:00 in the server's zone (`ROAMIND_TZ` overrides it; `NewsHours`, both DST offsets registered; add hours there for more digests a day). At a slot it starts one `news-digest` run per user with a channel, and a `news-channels` run for each user with memory but no channel yet.
- **The `news-digest` DAG** (`worker/workflows/news_digest.go`), serialized per user, with the same skip pattern as `learning-exercise`:
  1. **`check-skip`** skips users without a channel or active interests, and slots already posted (`digests` is unique per user and slot).
  2. **`gather`** fetches every feed of the user's active interests once, in parallel (`channels/feeds`, gofeed). Feed URLs can come from the agent and are fetched outside the sandbox, so the reader connects only to public addresses (loopback, private, CGNAT/tailnet, link-local and multicast are refused when dialled, which covers redirects and DNS) and reads at most 8 MB. It keeps items from the last 30 days (slow feeds such as Stoic writing post a few times a month), the newest 3 per feed, each link once, drops links already sent to the user (`sent_urls`), and keeps the newest 25 per interest, so the rubric does the filtering. A feed that fails is logged and skipped; if every feed fails, the task fails and is retried.
  3. **`rank`** runs one agent turn per interest (`agent/prompts/rank_digest.md`), three at a time, with structured output and no tools at all, in a throwaway directory with no saved session: the candidates are third-party text, so nothing in them can steer the turn at the user's files (`protocol.ToolsNone`). From each item's title, source, date and feed summary, and the slot's date as the coverage day, it returns per candidate: a 0-10 score for each criterion of the interest's rubric, `include` (whether it belongs in the digest at all), a `category` when the rubric has categories, a `story` slug naming the underlying event, and a short summary. Each turn is given the stories sent in the last 48 hours, slug and title, so the same event keeps its slug. An interest whose turn fails is left out of that digest; the task fails, and is retried, only when every interest fails.
  4. **`post`** keeps the included items whose combined score, the mean of their scores, is at least 5, nudged by feedback, and drops stories sent in the last 48 hours. It posts the best 4: one per story, at most 2 per interest and 1 per category. The message is plain: a header, then per item its title linked to the source, the summary and the interest, with link previews off. It reserves the slot (the `digests` row, unique per user and slot) before posting, then records the message, its items, and their links and stories as sent in one transaction, so a replayed run never posts twice; a failed Slack post gives the slot back. When nothing qualifies, nothing is posted. No retries.
- **Dedupe:** `sent_urls` holds `(user_id, url_hash)` for every link sent; the hash ignores the fragment, `utm_` parameters, a trailing slash and the case of scheme and host. `sent_stories` holds `(user_id, story)`, so one event reported by several sources makes one item; a story may return after 48 hours, when there is a new development in it.
- **Feedback** goes to `digest_signals`, and only the digest's owner counts:
  - **Reactions** to a digest (`reaction_added` → `slack:reaction` → `news-reaction`) count +1, -1 or 0 by emoji, for every item's interest.
  - **Replies** in a digest's thread are chat turns that also count +1; the turn starts a new session that is given the digest's text, so the agent can discuss the items.
  - In `post`, each net like over the last 30 days moves an interest's items up half a point, up to a point either way.
- **Slack setup:** nothing new; `reaction_added` and `reactions:read` were already in the manifest.
