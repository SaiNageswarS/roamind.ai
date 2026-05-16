# Plan: Personal Assistant v1 (Gateway + Agent)

Single-user in practice, **multi-tenant-ready by design**. A
**Gateway** fans remote channel messages (Telegram, Email, CLI) onto
a Redis stream. An **Agent** (Python + LangGraph) consumes them,
plans, calls tools, and emits user-facing responses back on a second
Redis stream, where the **Gateway** picks them up and routes them back
to the originating channel (or other channels as appropriate). Local
effects (open URL, send notification, TTS, clipboard) are returned as
text or rich responses through the channel—the user's OS/browser
handles the execution. **MongoDB Atlas** is the durable source of truth
for messages and memory (with Atlas Vector Search + Atlas Search for
hybrid retrieval); **Redis** is a hot, token-budgeted cache of the
recent conversation per user. Process supervision is handled by
**Process Compose** with **host-native Redis**.

## Boundary rule

> If the **user is the counterparty** → it's a **channel** (Gateway).
> If the **agent is the consumer of the result** → it's a **tool**
> (Agent side).

Corollary: `tasks.out` carries **user-facing responses only** (text,
links, instructions). Internal effects (browser scraping, DB reads, API
calls) are synchronous tool calls inside the agent's graph and never
touch Redis. Local OS effects (opening URLs, sending desktop
notifications, TTS) are returned as rich responses that the user can
act on immediately; the agent does not execute them.

## Architecture

```
 ┌────────────┐   ┌────────────┐   ┌────────────┐
 │ Telegram   │   │  Email     │   │  CLI       │
 │ (webhook)  │   │ (IMAP poll)│   │ (gRPC)     │
 └─────┬──────┘   └─────┬──────┘   └─────┬──────┘
       │                │                │
       └────────┬───────┴────────┬───────┘
                │ XADD tasks.in  │
                ▼                ▼
          ┌──────────────────────────────┐
          │   Gateway (Go)               │
          │  - channel I/O (in + out)    │
          │  - identity resolution       │
          │  - allowlist / authn         │
          │  - XREADGROUP tasks.out      │
          │  - respond back to channels  │
          └──────────┬───────────────────┘
                     │ XADD tasks.in
                     ▼
 ┌──────────────────────────────┐
 │ Redis Streams                    │
 │   tasks.in        (ingress)      │
 │   tasks.out       (agent output) │
 │   tasks.memorize  (extraction)   │
 │   tasks.dlq       (poison)       │
 │ Hot cache                        │
 │   chat:hot:{user_id}  LIST       │
 └──────┬───────────────────────┘
                     │ XREADGROUP tasks.in
                     ▼
   ┌──────────────────────────────────────────────┐
   │   Agent (Python + LangGraph)                 │
   │   graph: intake → plan → act → compact_err   │
   │                              → respond       │
   │                                              │
   │   ToolNode pulls from:                       │
   │   ├─ External MCP servers (via               │
   │   │   langchain-mcp-adapters)                │
   │   │   • Playwright browser (sandboxed)       │
   │   │   • Web search                           │
   │   │   • Filesystem (scoped)                  │
   │   └─ In-process Python tools                 │
   │       • reminders, notes, identity,          │
   │         memory recall (direct Mongo)         │
   └──────────┬──────────────────────────────────┘
              │ XADD tasks.out
              ▼
   ┌────────────────────────────────────────────┐
   │ Gateway (egress, all channels)             │
   │ • Match in_reply_to with original request  │
   │ • Telegram: send text/link                 │
   │ • Email: send reply                        │
   │ • CLI: stream response back                │
   └────────────────────────────────────────────┘

              ┌──────────────────────────────┐
              │ MongoDB Atlas                 │
              │  - users / identities         │
              │  - core_memory (always        │
              │    injected per user)         │
              │  - messages (full transcript) │
              │  - conversation_state         │
              │  - facts (vector + text idx)  │
              │  - category_summaries         │
              │  - reminders                  │
              │  - lg_checkpoints             │
              └──────────────────────────────┘
```

`tasks.out` envelopes are consumed by the Gateway consumer group,
which routes them back to the appropriate channel based on
`envelope.channel` and `envelope.in_reply_to`. Local effects ("open
this URL", "send notification", "speak") are returned as rich
responses in the user-facing message.

## Decisions

- **Two components:** Gateway (all channel I/O, ingress + egress),
  Agent (thinking). Both share the same Redis streams and envelope
  schema.
- **Process orchestration:** `process-compose` is the primary local
   runner for `redis`, `gateway`, `agent`, `memorizer`, and `scheduler`
   (single command, unified logs, restart policy).
- **Gateway framework:** `go-api-boot` with first-hand gRPC support;
  CLI channel uses gRPC server-side streaming for request-response.
- **Reply path:** `tasks.out` stream, routed by `channel`. Same path
  serves proactive/scheduled messages.
- **Tools — mixed strategy:**
  - Use `langchain-mcp-adapters` (`MultiServerMCPClient`) to consume
    **external MCP servers** for browser, web search, filesystem.
  - Implement **domain tools** (reminders, notes, identity, memory) as
    plain Python `@tool` functions with direct Mongo access — no MCP
    indirection where it adds no value.
  - One `ToolNode` receives both transparently.
- **Storage:** Mongo Atlas is the durable source of truth.
  Collections: `users`, `identities`, `core_memory`, `messages`,
  `conversation_state`, `facts` (vector + Atlas Search), `category_summaries`,
  `reminders`, `lg_checkpoints`. Redis is a hot cache only —
  losing it loses speed, not data.
- **Memory tiers:**
  - **Core** — small per-user doc (preferences, timezone, identity)
    always injected into the system prompt.
  - **Short-term** — Redis `chat:hot:{user_id}` LIST,
    **token-budgeted** (not fixed-K), evicted in turn-groups so AI
    replies never become orphans; rehydrated from Mongo `messages`
    on miss. All channels feed **one unified conversation per user**;
    replay is rendered with `[via {channel}]` prefix so the LLM sees
    channel switches.
  - **Long-term** — `facts` collection with embeddings + a **fixed
    taxonomy** of categories (`profile, preferences, food, hobbies,
    relationships, work, health, schedule, locations, goals, misc`).
    Facts carry `categories: list[str]` (soft-classification) and a
    `salience` score. Recall is **hybrid**: Atlas `$vectorSearch` +
    Atlas `$search` (BM25 on `statement`) merged via **Reciprocal
    Rank Fusion**. A separate **memorizer worker** consumes a
    `tasks.memorize` stream, extracts facts via an LLM prompt
    constrained to the taxonomy, dedupes by cosine ≥ 0.92, and
    refreshes per-category summaries.
  - **Category summaries** — one compact doc per `(user_id,
    category)`, regenerated from top-salience facts after each
    relevant change. **Non-empty summaries are injected at intake**
    so the assistant always opens with the right context.
- **LLM:** provider-agnostic `LLMClient` (OpenAI / Anthropic / Ollama),
  env-selectable.
- **LangGraph checkpointer:** custom Mongo checkpointer (~100 LOC) to
  keep the "Mongo only" promise.
- **Channels v1:** Telegram (webhook), Email (IMAP+SMTP), CLI.
  WhatsApp/Slack/Voice deferred.
- **Local effects v1 (Actuator):** open URL in default browser, OS
  desktop notification, TTS, clipboard set. Show-window deferred.
- **Browser duality:**
  - *Agent-driven* (headless Playwright MCP server, in a sandboxed
    Docker container, no host mounts) — for scraping/automation.
  - *User-facing* (Actuator opens URL in your real, logged-in browser)
    — for handoff.
  - These do **not** share code or sessions.
- **Contract:** one JSON Schema in `/proto/envelope.schema.json` →
  generated Go structs + Python pydantic models. Single source of truth.
- **Delivery:** at-least-once via Redis consumer groups; agent dedupes
  by `envelope.id`.
- **HITL / approvals:** agent emits `ask_user` intent on `tasks.out`
  and uses a LangGraph `interrupt` to pause; reply re-enters via
  `tasks.in`. Tools never call channels directly.
- **Error compaction:** dedicated graph node turns raw tool exceptions
  into short, structured, LLM-readable summaries before re-entering
  `plan`. Avoids polluting the context window with stack traces.
- **Secrets boundary:**
  - Gateway: bot token, SMTP creds.
  - Actuator: nothing sensitive (local OS APIs only).
  - Agent: LLM keys, MCP server credentials.
- **Multi-tenant readiness:** the agent is **user-agnostic compute**.
  It trusts `TaskIn.user_id` (gateway authenticates) and never does
  identity resolution itself. Every data path (Redis keys, Mongo
  queries, vector recall, checkpointer `thread_id`, logs) is keyed
  by `user_id`. **Tools receive `user_id` via LangGraph
  `RunnableConfig.configurable`, never as an LLM-generated
  argument** — this is the one rule that is expensive to retrofit
  and is enforced from v1. Auth, allowlist, invite-code onboarding,
  per-user rate limits, and per-user fairness on `tasks.in` are
  gateway-side concerns that can land later without touching the
  agent.

## Envelope (sketch)

```
TaskIn  { id, trace_id, user_id, channel, channel_msg_id,
          text, attachments[], received_at }

TaskOut { id, trace_id, in_reply_to, user_id,
          channel,            # telegram | email | cli
          chat_ref,           # channel-specific addressing
          intent,             # reply | ask_user
          payload,            # intent-specific (text with optional metadata)
                              # e.g., {text, action: "open_url", url: ...}
                              #    or {text, action: "notify", title: ...}
          created_at }
```

## gRPC CLI Service (sketch)

```proto
service AssistantCLI {
  // Client sends query, server streams responses (unidirectional stream).
  rpc Query(QueryRequest) returns (stream QueryResponse) {}
}

message QueryRequest {
  string id = 1;             // UUID for correlation
  string user_id = 2;        // canonical user (from allowlist)
  string text = 3;           // user query
  google.protobuf.Timestamp received_at = 4;
}

message QueryResponse {
  string id = 1;             # correlation ID (matches request)
  string reply = 2;          # response text from agent
  string intent = 3;         # reply | ask_user | ...
  google.protobuf.Timestamp created_at = 4;
}
```

**Flow:** CLI → `Query(req)` → Gateway enqueues to `tasks.in` + registers correlation → 
Agent processes → emits `tasks.out` with `in_reply_to=req.id` → 
Gateway (egress) matches, streams `QueryResponse` to the client → client exits.

## Phases & steps

### Phase 1 — Contracts & infra skeleton
1. Define envelope JSON Schema in `/proto/envelope.schema.json`.
2. Define gRPC CLI service in `/proto/cli.proto` (AssistantCLI service
   with unidirectional `Query` stream).
3. Generate Go structs (`/gateway/internal/envelope`,
   `/actuator/internal/envelope`) and Python pydantic models
   (`/agent/app/envelope.py`); generate gRPC stubs from `.proto`.
4. `process-compose.yaml` with `redis` (host-native `redis-server`),
   `gateway`, `agent`, `actuator`, `scheduler` and optional
   `mcp-browser` (sandboxed Playwright via local process or docker-run).
5. `.env.example` (Atlas URI, bot token, LLM keys, MCP endpoints).
6. `Makefile`: `up`/`down` wrappers for `process-compose`, plus
   `lint`, `test`, `gen` (runs `protoc` for gRPC stubs).

### Phase 2 — Gateway (Go, go-api-boot)  *parallel with Phase 3 & 4*
1. Skeleton: `cmd/gateway/main.go`, env config, zerolog, graceful
   shutdown. Initialize `go-api-boot` with gRPC server.
2. Redis wrapper: `XADD tasks.in`, `XREADGROUP tasks.out` (channel
   group: telegram/email/cli).
3. Channel adapter interface `Adapter { Start(ctx); Send(TaskOut) }`.
4. Telegram adapter: webhook handler → envelope → `XADD`; Bot API send
   on `tasks.out`.
5. Email adapter: IMAP IDLE/poll → envelope; SMTP send outbound.
6. **CLI adapter (gRPC):** Implement `AssistantCLI.Query` server-side
   streaming RPC. On client `Query(req)` call: (a) generate UUID, (b)
   `XADD tasks.in` with `id=UUID, channel=cli`, (c) register
   correlation map `UUID → stream`, (d) keep stream open waiting for
   `tasks.out` with `in_reply_to=UUID`. On `XREADGROUP tasks.out`
   match, stream `QueryResponse` to client and close stream. Timeout
   after configurable TTL (e.g., 30s).
7. Identity resolver: `(channel, channel_user_id)` → canonical
   `user_id` via Mongo `identities`.
8. Allowlist middleware: reject unknown users politely.
9. Per-user rate limit (token bucket in Redis).

### Phase 3 — Agent (Python + LangGraph)  *parallel with Phase 2 & 4*
1. Skeleton: `app/main.py` worker loop, `XREADGROUP tasks.in`, `XACK`
   on success, push to `tasks.dlq` after N failures.
2. `LLMClient` interface + adapters for OpenAI, Anthropic, Ollama.
3. MCP client setup: `MultiServerMCPClient` configured for
   `mcp-browser`, `mcp-search`, `mcp-fs` (paths scoped to a workspace
   dir).
4. In-process tools (`@tool`): `set_reminder`, `list_reminders`,
   `note_add`, `note_search`, `recall_facts`, `update_core_memory`,
   `ask_user`.
5. LangGraph graph:
   - `intake` — load core memory + short-term turns + relevant facts.
   - `plan` — LLM chooses tool call or direct response.
   - `act` — `ToolNode` over MCP + in-process tools.
   - `compact_err` — normalize exceptions into LLM-friendly summaries
     (factor 9 of 12-factor agents).
   - `respond` — format reply, persist turn, emit `tasks.out`.
   - Supports `interrupt` for `ask_user` HITL.
6. Mongo checkpointer (`lg_checkpoints` collection).
7. Memory module (`agent/app/memory/`):
   - `core.py` — get/set per-user core memory, always injected.
   - `messages.py` — append-only Mongo transcript (per-message
     `token_count` via `tiktoken` with model-correct encoding).
   - `hotcache.py` — Redis `chat:hot:{user_id}` LIST, token-budgeted,
     **turn-group eviction**, Mongo rehydrate on miss.
   - `embeddings.py` — provider-agnostic embedder interface.
   - `facts.py` — `upsert`, `dedupe` (cosine ≥ 0.92), `recall` using
     **hybrid search** (vector + BM25 + Reciprocal Rank Fusion),
     `user_id` pre-filter inside the `$vectorSearch` stage.
   - `summaries.py` — per-category compact summary, refreshed from
     top-salience facts after changes.
   - `extractor.py` — taxonomy-constrained LLM fact extractor with
     few-shot examples; emits `categories[]`, `statement`,
     `salience`, `confidence`.
8. Memorizer worker (`agent/app/memorizer.py`): separate process,
   `XREADGROUP tasks.memorize` (group `memorizer`); for each turn-pair
   fetches messages, runs the extractor, upserts facts, refreshes
   affected category summaries. Added as a `memorizer` service in
   `process-compose.yaml`.
9. Atlas index definitions checked in at `agent/atlas_indexes.json`
   (vector index on `facts.embedding`; Atlas Search on
   `facts.statement` + `facts.categories`).

### Phase 4 — Gateway egress (expand Phase 2)
1. In `XREADGROUP tasks.out`, match `in_reply_to` with pending CLI
   streams and pending email/Telegram request IDs.
2. Format response:
   - **CLI:** if intent is `reply`, stream text back. If action is
     `open_url`, include `{action: "open_url", url: ...}` in payload;
     user can decide to open or copy-paste. If action is `notify` or
     `speak`, include as metadata in payload.
   - **Telegram/Email:** plain text. Actions ("open this", "notify",
     "speak") rendered as text instructions, e.g., "👉 [Open
     dashboard](https://...)".
3. Send via appropriate channel adapter (Telegram API, SMTP, CLI
   stream).

### Phase 5 — Scheduler & proactive path
1. Small Go service: reads `reminders` from Mongo, at fire-time
   `XADD tasks.in` with `channel=cli` (or preferred channel) envelope
   so the agent composes the user-facing message and routes it via
   `tasks.out` back to that channel.

### Phase 6 — Observability & hardening
1. OpenTelemetry SDK in Gateway and Agent; `trace_id` propagates
   through every envelope. Console exporter for v1.
2. `tasks.dlq` + a tiny CLI to inspect/redrive.
3. Health endpoints (`/healthz`, `/readyz`) on Gateway.
4. Browser MCP sandbox hardening: no host mounts, network egress
   limited, per-session ephemeral profile.
5. Email anti-loop guards: `In-Reply-To` allowlist, per-thread reply
   cap, drop auto-submitted (`Auto-Submitted: auto-*`) messages.
6. Treat all channel content as untrusted data (prompt-injection
   defense in system prompt).
7. Gateway anti-loop: do not echo `tasks.out` back to `tasks.in`;
   avoid turning the agent into a feedback loop.

### Phase 7 — Verification
1. Unit: envelope round-trip Go↔Python, identity resolution, LLM
   client mocks, fact retrieval, checkpointer, gRPC CLI stubs.
2. Integration: `process-compose up`; gRPC CLI client sends
   "remind me to drink water in 1m" → reminder fires → response streams
   back via gRPC.
3. Telegram smoke test against a test bot.
4. Email smoke test against a throwaway mailbox.
5. Browser-tool smoke test: agent uses MCP browser to fetch a page
   and summarize.
6. CLI intent test: agent emits action="open_url" → user receives
   link in CLI response.
7. HITL test: agent invokes `ask_user`, user replies via CLI/Telegram,
   graph resumes.
8. Resilience: kill agent mid-task, restart, assert idempotent
   reprocessing (no duplicate side effects).

## Relevant files (to be created)

- `/proto/envelope.schema.json` — single source of truth contract.
- `/proto/cli.proto` — gRPC AssistantCLI service definition.
- `/gateway/cmd/gateway/main.go` — entrypoint.
- `/gateway/internal/adapters/{telegram,email,cli}.go`.
- `/gateway/internal/stream/redis.go`.
- `/gateway/internal/identity/resolver.go`.
- `/agent/app/main.py` — worker loop (`tasks.in` → graph → `tasks.out`).
- `/agent/app/memorizer.py` — worker loop (`tasks.memorize` →
  extractor → facts/summaries).
- `/agent/app/graph.py` — LangGraph definition.
- `/agent/app/checkpoint_mongo.py` — custom Mongo checkpointer
  (`thread_id = user_id`).
- `/agent/app/db.py` — Mongo client + index assertions.
- `/agent/app/memory/{models,core,messages,hotcache,embeddings,facts,summaries,extractor}.py`.
- `/agent/atlas_indexes.json` — vector + Atlas Search index defs.
- `/agent/app/llm/{base,openai,anthropic,ollama}.py`.
- `/agent/app/tools/{reminders,notes,identity,recall,ask_user}.py`.
- `/agent/app/mcp_clients.py` — `MultiServerMCPClient` wiring.
- `/scheduler/cmd/scheduler/main.go` — reminder ticker.
- `/process-compose.yaml`, `/.env.example`, `/Makefile`.

## Scope boundaries

**In v1:** Telegram + Email + CLI (gRPC); reminders + notes + recall
tools; MCP browser + web search + filesystem; vector memory + core
memory + categorized facts + hot cache; memorizer worker; allowlisted
single user; process-compose + host-native redis; local effects
(open-URL, notify, TTS, clipboard) returned as rich responses to user.

**Deferred (multi-user phase, no agent change needed):** invite-code
onboarding, per-user fairness on `tasks.in` (in-flight semaphore
then hash-partitioned streams), `users`/`invites`/`usage` collections,
memorizer batching, per-user token/cost quotas, email anti-spoof
(reply-tokenized addresses).

**Deferred (other):** WhatsApp/Slack/Voice; real calendar
integration; web UI; rich attachment handling; evaluator-optimizer
self-critique; multi-agent handoffs;
fine-tuned/locally-served models beyond Ollama; Temporal/NATS
migration; explicit fact-invalidation flag; `forget` tool.

## Design validation

Aligned with established patterns:

- **Hexagonal / Ports-and-Adapters** — Agent is the domain core;
  Gateway, Actuator, MCP servers, and Mongo are adapters.
- **Anthropic "Building Effective Agents"** — orchestrator-workers
  pattern; start simple; observable loop.
- **OpenHands / OpenDevin** — sandboxed runtime for the browser tool
  (we mirror their isolation discipline).
- **MCP (Model Context Protocol)** — external tools consumed via the
  standard; domain tools kept in-process where MCP adds no value.
- **Letta / MemGPT** — explicit memory tiers (core / short-term /
  long-term).
- **12-factor agents** — own prompts, own context, own control flow,
  HITL via tool calls, error compaction, trigger-from-anywhere.

Deliberate non-alignments (correct for v1): no multi-agent handoffs
(Swarm/AutoGen/CrewAI), no evaluator-optimizer self-critique, no
code-execution sandbox.

## Further considerations

0. **Atlas tier prereq.** Atlas Vector Search requires **M10+**
   (~$57/mo); free/shared tiers do not support it. Confirm cluster
   tier before starting the memory work. Fallbacks if staying on
   free: local Qdrant or pgvector — keep the embedder + recall
   interfaces abstracted so swapping is mechanical.
1. **LangGraph checkpointer in Mongo.** Stock checkpointers are
   Postgres/SQLite. Writing a small custom Mongo adapter keeps the
   storage promise; alternative is to drop the checkpointer and
   manage thread state ourselves in `conversation_state`. *Recommend
   custom adapter; `thread_id = user_id`.*
2. **Email ping-pong risk.** Auto-replies to lists/bounces. Guard with
   `In-Reply-To` allowlist, per-thread reply cap, `Auto-Submitted`
   header filtering.
3. **Prompt injection from channel content.** Treat all message bodies
   as untrusted data. System prompt explicitly sandboxes channel text.
4. **Browser-tool blast radius.** Sandboxed container; ephemeral
   profile; constrained network egress; no host mounts.
5. **Fact contradictions.** New fact ("I'm vegetarian now") may
   contradict old ("loves sushi"). v1 stores both with timestamps;
   the per-category summary prompt prefers newer. Explicit
   invalidation flag deferred.
6. **Embedding model.** Start with OpenAI `text-embedding-3-small`
   for quality; keep the `embeddings.py` interface so swapping to a
   local model (`bge-small`) is mechanical.
7. **Memorizer cost at scale.** Per-turn extraction is fine
   single-user. At multi-user scale, batch by user every N turns or
   every 5 minutes — no schema change required.
8. **Local actions without an Actuator.** The agent emits
   `TaskOut.payload` with optional `action` metadata (e.g.,
   `{text: "...", action: "open_url", url: ".."}`) but never
   executes them. The Gateway delivers the response to the user, who
   can act on it (copy link, follow instruction, etc.). This shifts
   trust to the user's judgment rather than background daemons.

## TODOs

- **Agent and Gateway streaming support (v2):** Currently `CliService.Query()` is
  request-response (one-shot). For agents that emit multiple messages
  per query, implement streaming so the client receives partial results
  as they arrive. Agent side: emit multiple `TaskOut` envelopes with
  streaming metadata. Gateway side: loop in `Query` handler to receive
  and stream each message to client. Requires: (1) keep pending entry
  alive until end-of-stream, (2) add `Done` flag or marker in `TaskOut`
  to signal completion, (3) ensure XREAD consumes all streaming chunks
  for a single query ID.
