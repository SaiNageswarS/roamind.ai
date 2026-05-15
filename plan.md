# Plan: Personal Assistant v1 (Gateway + Agent + Actuator)

Single-user, laptop-deployed assistant. A **Gateway** fans remote
channel messages (Telegram, Email, CLI) onto a Redis stream. An
**Agent** (Python + LangGraph) consumes them, plans, calls tools, and
emits user-facing intents back on a second Redis stream. An
**Actuator** (local laptop daemon) consumes those intents and produces
local effects (open URL in real Chrome, desktop notifications, TTS).
MongoDB Atlas (with Atlas Vector Search) is the only memory store.
Process supervision is handled by **Process Compose** with
**host-native Redis**.

## Boundary rule

> If the **user is the counterparty** → it's a **channel** (Gateway or
> Actuator). If the **agent is the consumer of the result** → it's a
> **tool** (Agent side).

Corollary: `tasks.out` carries **user-facing deliveries only**.
Internal effects (browser scraping, DB reads, API calls) are
synchronous tool calls inside the agent's graph and never touch Redis.

## Architecture

```
 ┌────────────┐   ┌────────────┐   ┌────────────┐
 │ Telegram   │   │  Email     │   │  CLI       │
 │ (webhook)  │   │ (IMAP poll)│   │ (stdin)    │
 └─────┬──────┘   └─────┬──────┘   └─────┬──────┘
       └────────┬───────┴────────┬───────┘
                ▼                ▼
          ┌──────────────────────────┐
          │   Gateway (Go)           │
          │  - remote channel I/O    │
          │  - identity resolution   │
          │  - allowlist / authn     │
          └──────────┬───────────────┘
                     │ XADD tasks.in
                     ▼
              ┌──────────────┐
              │ Redis Streams│  tasks.in   (ingress → agent)
              │              │  tasks.out  (agent  → deliveries)
              │              │  tasks.dlq  (poison messages)
              └──────┬───────┘
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
   └──────────┬─────────────────────────┬─────────┘
              │ XADD tasks.out          │
              ▼                         ▼
   ┌────────────────────┐      ┌──────────────────┐
   │ Gateway (egress)   │      │ Actuator (Go)    │
   │ (remote channels)  │      │ (local laptop)   │
   │ • Telegram send    │      │ • open URL in    │
   │ • SMTP send        │      │   real browser   │
   │ • CLI echo         │      │ • desktop notif  │
   └────────────────────┘      │ • TTS / sound    │
                               │ • clipboard      │
                               └──────────────────┘

              ┌──────────────────────────┐
              │ MongoDB Atlas             │
              │  - users / identities     │
              │  - core_memory (always    │
              │    injected per user)     │
              │  - conversations / msgs   │
              │  - facts (vector index)   │
              │  - reminders              │
              │  - lg_checkpoints         │
              └──────────────────────────┘
```

`tasks.out` envelopes are partitioned by `envelope.channel`:
- `telegram`, `email`, `cli` → Gateway consumer group.
- `local.browser`, `local.notify`, `local.tts` → Actuator consumer group.

## Decisions

- **Three components:** Gateway (remote I/O), Agent (thinking), Actuator
  (local effects). All share the same Redis streams and envelope schema.
- **Process orchestration:** `process-compose` is the primary local
   runner for `redis`, `gateway`, `agent`, `actuator`, and `scheduler`
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
- **Storage:** Mongo Atlas only. Collections: `users`, `identities`,
  `core_memory`, `conversations`, `messages`, `facts` (vector index),
  `reminders`, `lg_checkpoints`.
- **Memory tiers:**
  - **Core** — small per-user doc (preferences, timezone, identity)
    always injected into the system prompt.
  - **Short-term** — last N turns of the current thread.
  - **Long-term** — `facts` collection with embeddings, recalled via
    Atlas `$vectorSearch`.
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

## Envelope (sketch)

```
TaskIn  { id, trace_id, user_id, channel, channel_msg_id,
          text, attachments[], received_at }

TaskOut { id, trace_id, in_reply_to, user_id,
          channel,            # telegram | email | cli |
                              # local.browser | local.notify | local.tts
          chat_ref,           # channel-specific addressing
          intent,             # reply | ask_user | open_url | notify | speak
          payload,            # intent-specific (text, url, title, body…)
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
7. Memory module:
   - `CoreMemory` — get/set per-user, always injected into system prompt.
   - `ConversationStore` — last N turns per thread.
   - `FactStore` — embeddings → Atlas `$vectorSearch`.
   - Background `summarize_and_extract` job after each turn.

### Phase 4 — Actuator (Go, host-local)  *parallel with Phase 2 & 3*
1. Skeleton: `cmd/actuator/main.go`, runs in user session (systemd
   `--user` unit or launchd plist).
2. Redis consumer (`XREADGROUP tasks.out`, group `actuator`, filter on
   `channel = local.*`).
3. Handlers:
   - `open_url` → `xdg-open` / `open` / `cmd /c start`.
   - `notify` → `libnotify` / `osascript` / Windows toast.
   - `speak` → system TTS (`espeak-ng` / `say` / SAPI).
   - `set_clipboard` → `xclip` / `pbcopy` / Win32.
4. Health endpoint on a local-only port.

### Phase 5 — Scheduler & proactive path
1. Small Go service: reads `reminders` from Mongo, at fire-time
   `XADD tasks.in` with `channel=system` envelope so the agent
   composes the user-facing message and routes it via `tasks.out`.

### Phase 6 — Observability & hardening
1. OpenTelemetry SDK in Gateway, Agent, Actuator; `trace_id`
   propagates through every envelope. Console exporter for v1.
2. `tasks.dlq` + a tiny CLI to inspect/redrive.
3. Health endpoints (`/healthz`, `/readyz`) on Gateway and Actuator.
4. Browser MCP sandbox hardening: no host mounts, network egress
   limited, per-session ephemeral profile.
5. Email anti-loop guards: `In-Reply-To` allowlist, per-thread reply
   cap, drop auto-submitted (`Auto-Submitted: auto-*`) messages.
6. Treat all channel content as untrusted data (prompt-injection
   defense in system prompt).

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
6. Actuator smoke test: agent asks to "open my dashboard" → real
   Chrome opens.
7. HITL test: agent invokes `ask_user`, user replies via gRPC, graph
   resumes.
8. Resilience: kill agent mid-task, restart, assert idempotent
   reprocessing (no duplicate side effects).

## Relevant files (to be created)

- `/proto/envelope.schema.json` — single source of truth contract.
- `/proto/cli.proto` — gRPC AssistantCLI service definition.
- `/gateway/cmd/gateway/main.go` — entrypoint.
- `/gateway/internal/adapters/{telegram,email,cli}.go`.
- `/gateway/internal/stream/redis.go`.
- `/gateway/internal/identity/resolver.go`.
- `/actuator/cmd/actuator/main.go`.
- `/actuator/internal/handlers/{open_url,notify,speak,clipboard}.go`.
- `/agent/app/main.py` — worker loop.
- `/agent/app/graph.py` — LangGraph definition.
- `/agent/app/checkpoint_mongo.py` — custom Mongo checkpointer.
- `/agent/app/memory/{core.py,conversations.py,facts.py}`.
- `/agent/app/llm/{base,openai,anthropic,ollama}.py`.
- `/agent/app/tools/{reminders,notes,identity,recall,ask_user}.py`.
- `/agent/app/mcp_clients.py` — `MultiServerMCPClient` wiring.
- `/scheduler/...` — reminder ticker.
- `/process-compose.yaml`, `/.env.example`, `/Makefile`.

## Scope boundaries

**In v1:** Telegram + Email + CLI; Actuator with open-URL, notify,
TTS, clipboard; reminders + notes + recall tools; MCP browser + web
search + filesystem; vector memory + core memory; allowlisted single
user; process-compose + host-native redis + host-side actuator.

**Deferred:** WhatsApp/Slack/Voice; multi-tenant; real calendar
integration; web UI; rich attachment handling; show-window actuator;
evaluator-optimizer self-critique; multi-agent handoffs;
fine-tuned/locally-served models beyond Ollama; Temporal/NATS
migration.

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

1. **LangGraph checkpointer in Mongo.** Stock checkpointers are
   Postgres/SQLite. Writing a small custom Mongo adapter keeps the
   storage promise; alternative is to drop the checkpointer and
   manage thread state ourselves in `conversations`. *Recommend custom
   adapter.*
2. **Email ping-pong risk.** Auto-replies to lists/bounces. Guard with
   `In-Reply-To` allowlist, per-thread reply cap, `Auto-Submitted`
   header filtering.
3. **Prompt injection from channel content.** Treat all message bodies
   as untrusted data. System prompt explicitly sandboxes channel text.
4. **Browser-tool blast radius.** Sandboxed container; ephemeral
   profile; constrained network egress; no host mounts.
5. **Actuator privilege.** Runs in the user session — has access to
   your real browser profile, clipboard, audio. Treat its inbound
   envelopes as authenticated only because they originate from the
   agent we own. Do not expose `tasks.out` to untrusted producers.

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
