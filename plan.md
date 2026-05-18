# Roamind: Personal Assistant — Current State & Roadmap

Single-user in practice, **multi-tenant-ready by design**. A **Gateway**
(Go) bridges remote channels (Telegram, CLI/gRPC) onto a Redis stream.
An **Agent** (Python + LangGraph) consumes them, plans, calls tools,
and emits responses back on a second Redis stream, where the gateway
routes them to the originating channel. **MongoDB Atlas** holds
long-term memory (typed user profile, a `$text`-indexed knowledge base,
and a time-series of events); **Redis** is the hot conversation cache
plus a content-addressable LLM response cache. Local orchestration is
**process-compose** with host-native Redis; production runs on
**Azure Container Apps** as a single Container App with three
co-located containers (`gateway`, `agent`, `redis`) sharing loopback.

## Boundary rule

> If the **user is the counterparty** → it's a **channel** (Gateway).
> If the **agent is the consumer of the result** → it's a **tool**
> (Agent side).

Corollary: `tasks.out` carries **user-facing responses only**. Internal
effects (web search, finance quotes, place lookups) are synchronous
tool calls inside the agent's graph and never touch Redis.

## Architecture

```
 ┌────────────┐                       ┌────────────┐
 │ Telegram   │                       │  CLI       │
 │ (long-poll │                       │ (gRPC      │
 │  Bot API)  │                       │  streaming)│
 └─────┬──────┘                       └─────┬──────┘
       │                                    │
       └────────────────┬───────────────────┘
                        │ XADD tasks.in
                        ▼
         ┌──────────────────────────────────┐
         │  Gateway (Go, go-api-boot)       │
         │  • cli_service.go (gRPC)         │
         │  • telegram.go    (Bot API poll) │
         │  • dispatcher.go  (egress mux)   │
         │  • stream.go      (Redis I/O)    │
         │  • identity via `logins` Mongo   │
         └──────────┬───────────────────────┘
                    │
                    ▼
        ┌─────────────────────────────────┐
        │ Redis Streams                   │
        │   tasks.in   (ingress)          │
        │   tasks.out  (agent → gateway)  │
        │   tasks.dlq  (poison)           │
        │ Hot conversation cache          │
        │   chat:hot:{user_id}   LIST     │
        │ LLM response cache              │
        │   llm:cache:v1:{user_id}:{hash} │
        └──────┬──────────────────────────┘
                    │ XREADGROUP tasks.in
                    ▼
   ┌──────────────────────────────────────────────┐
   │   Agent (Python + LangGraph)                 │
   │   graph: intake → plan → act → compact_err   │
   │                              → respond       │
   │                                              │
   │   ToolNode tools (in-process @tool):         │
   │   • web_search       (Exa /search)           │
   │   • github_search    (Exa, category=github)  │
   │   • news_search      (Exa, category=news)    │
   │   • research_search  (Exa, research paper)   │
   │   • finance_search   (Yahoo Finance quote)   │
   │   • maps_search      (Google Places New)     │
   └──────────┬───────────────────────────────────┘
              │ XADD tasks.out
              ▼
   ┌────────────────────────────────────────────┐
   │ Gateway egress (EgressDispatcher)          │
   │ • XREADGROUP tasks.out                     │
   │ • Match in_reply_to → pending CLI streams  │
   │ • Telegram: sendMessage(chat_id)           │
   └────────────────────────────────────────────┘

              ┌──────────────────────────────┐
              │ MongoDB Atlas                │
              │  • user_profiles (typed,     │
              │    always injected at intake)│
              │  • knowledge ($text indexed) │
              │  • events    (time-series)   │
              │  • logins    (channel ↔ user)│
              └──────────────────────────────┘
```

## Decisions (as implemented)

- **Two components:** Gateway (channel I/O, ingress + egress) and
  Agent (thinking). Share Redis streams and the envelope schema.
- **Process orchestration (local):** `process-compose.yaml` runs
  `redis`, `gateway`, and `agent`. Single command, unified logs,
  restart-on-failure.
- **Gateway framework:** `go-api-boot` with first-class gRPC. CLI
  channel uses gRPC server-side streaming for request-response.
- **Reply path:** `tasks.out` stream, routed by `channel` and
  `in_reply_to`. Same path will serve future proactive messages.
- **Channels v1:** Telegram (Bot API long-poll, identity resolved via
  Mongo `logins`) and CLI (gRPC). Email is deferred.
- **Tools v1 — pure HTTP, in-process:** Exa for web/github/news/
  research search, Yahoo Finance for live quotes, Google Places (New)
  for place lookups. All tools convert predictable upstream errors
  into short LLM-readable strings so a tool failure does not abort the
  turn (`compact_err` still handles unexpected exceptions). MCP
  adapters and domain tools (reminders, notes, recall, ask_user) are
  deferred.
- **LLM:** Anthropic Claude via `langchain-anthropic`
  (`claude-sonnet-4-6` by default). Wrapped in `LLMClient` with a
  **Redis content-addressable response cache** — sha256 over the
  current turn (last `HumanMessage` onwards), bound tool identities,
  and model parameters; scoped per `user_id`; TTL configurable
  (`cache_ttl_seconds`, default 24h). Provider-agnostic interface
  (OpenAI / Ollama) is wired as an optional extra but not yet active.
- **Storage:** Mongo holds long-term memory; Redis holds short-term
  conversation history plus the LLM cache. Losing Redis loses recent
  history and cache, not durable data.
- **Memory tiers (implemented):**
  - **Profile** — typed per-user document (`UserProfile`) with
    identity, location, profession, work experience, education, and a
    free-form `extras` bag. The `intake` node renders it into the
    system prompt every turn (replaces what was previously called
    "core memory").
  - **Short-term** — Redis LIST `chat:hot:{user_id}`, fixed-K
    (`short_term_max_messages`, default 8). Channel switches are
    surfaced with a `[via {channel}]` prefix so the LLM sees the
    medium change.
  - **Knowledge** — per-user docs in `knowledge`, Mongo `$text` index
    on `(title, content)`.
  - **Events** — per-user time-series in `events`, indexed on
    `(user_id, occurred_at)` and `(user_id, kind, occurred_at)`.
- **Multi-tenant readiness:** `user_id` propagates through every
  envelope, Redis key, Mongo query, identity resolution, and LLM
  cache scope. Tools-receive-`user_id`-via-`RunnableConfig.configurable`
  will be enforced when domain tools land.
- **Delivery:** at-least-once via Redis consumer groups; agent moves
  to `tasks.dlq` after `MAX_DELIVERIES = 5`.
- **Error compaction:** `compact_err` node turns raw tool exceptions
  into short, structured, LLM-readable summaries before re-entering
  `plan`.
- **Secrets boundary:**
  - Gateway: `TELEGRAM_BOT_TOKEN`, `ACCESS-SECRET` (JWT signing).
  - Agent: `ANTHROPIC_API_KEY`, `EXA_API_KEY`, `GOOGLE_MAPS_API_KEY`,
    `MONGO_URI`.
- **Treat channel content as untrusted data** — the system prompt
  explicitly sandboxes it against prompt injection.

## Envelope

```
TaskIn  { id, trace_id, user_id, channel, channel_msg_id,
          text, attachments[], received_at }

TaskOut { id, trace_id, in_reply_to, user_id,
          channel,            # telegram | cli
          chat_ref,           # channel-specific addressing
          intent,             # reply | ask_user
          payload,            # response text
          created_at }
```

Wire format is `protojson` (lowerCamelCase keys, RFC3339 timestamps)
so payloads round-trip between Go and Python.

## gRPC CLI Service

```proto
service AssistantCLI {
  rpc Query(QueryRequest) returns (stream QueryResponse) {}
}
```

**Flow:** CLI → `Query(req)` → Gateway `XADD tasks.in` + registers
correlation → Agent processes → `XADD tasks.out` with
`in_reply_to=req.id` → `EgressDispatcher` matches, streams
`QueryResponse` to the client, closes the stream. Currently
request-response (one envelope in, one envelope out); multi-message
streaming is a v2 TODO.

## Deployment — Azure Container Apps

Single Container App, single revision, three co-located containers
sharing a loopback network namespace so the gateway and the agent both
reach Redis at `redis://localhost:6379`:

| Container | Role                          | Ports                |
|-----------|-------------------------------|----------------------|
| `gateway` | gRPC ingress, Telegram poller | `50051` (gRPC), `8081` |
| `agent`   | LangGraph worker              | none (Redis streams) |
| `redis`   | Task broker / cache           | `6379` (loopback)    |

- **Images:** `roamind-gateway` built from the repo root (needs
  `proto/`), `roamind-agent` built from `./agent`, both pushed to
  Docker Hub.
- **Provisioning** via `az containerapp` — resource group
  `roamind-rg`, environment `roamind-env`, region `centralindia`.
  Initial revision is created with the gateway image; the `agent` and
  `redis` sidecars are added by editing the revision YAML in the
  portal.
- **Ingress:** external, HTTP/2, target port `50051`. Min replicas 1,
  max 3, `0.5` CPU / `1.0 GiB` memory.
- **Required env vars:** `REDIS_URL` (defaults to loopback),
  `ACCESS-SECRET`, `TELEGRAM_BOT_TOKEN`, `MONGO_URI`,
  `ANTHROPIC_API_KEY`, `EXA_API_KEY`, `GOOGLE_MAPS_API_KEY`.
- **MongoDB Atlas** is external (not in ACA); reachable from the
  agent over the public connection string.
- **Known limitation:** the local CLI dials with
  `insecure.NewCredentials()`, so the public ACA TLS ingress is not
  yet reachable from `roamind-cli` — CLI must learn to dial over
  TLS before remote use.

Runbook: [deploy/README.md](deploy/README.md).

## Implemented

- Gateway: gRPC `AssistantCLI.Query` streaming endpoint
  ([gateway/services/cli_service.go](gateway/services/cli_service.go));
  Telegram Bot API long-poll
  ([gateway/services/telegram.go](gateway/services/telegram.go));
  egress mux ([gateway/services/dispatcher.go](gateway/services/dispatcher.go));
  Redis stream helpers ([gateway/services/stream.go](gateway/services/stream.go));
  identity model ([gateway/db/login_model.go](gateway/db/login_model.go)).
- Agent worker loop ([agent/app/main.py](agent/app/main.py)):
  `XREADGROUP tasks.in` → graph → `XADD tasks.out`, DLQ after 5
  failed deliveries.
- LangGraph graph ([agent/app/graph.py](agent/app/graph.py)):
  `intake → plan → act → compact_err → respond`.
- LLM client with per-user Redis response cache
  ([agent/app/llm.py](agent/app/llm.py)).
- Tools ([agent/app/tools.py](agent/app/tools.py)): `web_search`,
  `github_search`, `news_search`, `research_search` (Exa);
  `finance_search` (Yahoo Finance); `maps_search` (Google Places New).
- Memory:
  - Short-term Redis LIST
    ([agent/app/memory/short_term.py](agent/app/memory/short_term.py)).
  - Long-term Mongo stores
    ([agent/app/memory/long_term.py](agent/app/memory/long_term.py)):
    `user_profiles` (typed `UserProfile`), `knowledge`
    (`$text`-indexed), `events` (time-series).
  - Models ([agent/app/memory/models.py](agent/app/memory/models.py)).
- Envelope round-trip Go ↔ Python
  ([agent/app/envelope.py](agent/app/envelope.py),
  [proto/envelope.proto](proto/envelope.proto)).
- Local orchestration ([process-compose.yaml](process-compose.yaml)).
- Azure Container Apps deployment runbook
  ([deploy/README.md](deploy/README.md)).

## Next up

1. **CLI TLS.** Replace `insecure.NewCredentials()` in `roamind-cli`
   with a TLS dial so the public ACA ingress works end-to-end.
2. **Domain tools.** Add `set_reminder` / `list_reminders` /
   `note_add` / `note_search` / `recall` (over the existing
   `events` + `knowledge` collections) / `ask_user` (HITL via
   LangGraph `interrupt`). When these land, switch to receiving
   `user_id` through `RunnableConfig.configurable` rather than
   relying on LLM-generated arguments.
3. **Provider-agnostic LLM.** Activate the OpenAI / Ollama adapters
   already listed as optional extras in
   [agent/pyproject.toml](agent/pyproject.toml).
4. **Observability.** OpenTelemetry SDK in both components; propagate
   `trace_id` through every envelope; console exporter for v1.
5. **Streaming responses.** Multi-message `tasks.out` for partial
   agent results (see TODO at end of file).

## Deferred

- **Email channel** (IMAP poll + SMTP send), with anti-loop guards
  (`In-Reply-To` allowlist, per-thread reply cap, `Auto-Submitted`
  filtering).
- **MCP integration** via `langchain-mcp-adapters` —
  sandboxed Playwright browser, web-search server, scoped filesystem
  server.
- **Vector + hybrid long-term memory.** Replace / augment `knowledge`
  with a `facts` collection: Atlas Vector Search on `embedding` +
  Atlas Search BM25 on `statement`, merged via Reciprocal Rank
  Fusion; fixed taxonomy
  (`profile, preferences, food, hobbies, relationships, work, health,
  schedule, locations, goals, misc`); per-fact `salience` and
  `categories[]`. Requires Atlas M10+.
- **Memorizer worker.** Separate process consuming
  `tasks.memorize`; taxonomy-constrained LLM extractor; dedupe by
  cosine ≥ 0.92; per-category summary doc refreshed after each
  relevant change and injected at intake.
- **Token-budgeted short-term** with turn-group eviction and Mongo
  rehydrate on miss.
- **Custom Mongo LangGraph checkpointer** (`lg_checkpoints`,
  `thread_id = user_id`) to support `interrupt`-based HITL and
  cross-turn resumption.
- **Scheduler** — Go service reading `reminders`, firing into
  `tasks.in` at the right channel.
- **Actuator / local effects daemon** — open URL / OS notification /
  TTS / clipboard. v1 returns these as rich text the user acts on
  themselves.
- **Per-user fairness** on `tasks.in` (in-flight semaphore, then
  hash-partitioned streams); per-user token/cost quotas; invite-code
  onboarding; `users` / `invites` / `usage` collections.
- **More channels** — WhatsApp, Slack, Voice.

## Relevant files

- Gateway: [gateway/main.go](gateway/main.go),
  [gateway/services/cli_service.go](gateway/services/cli_service.go),
  [gateway/services/telegram.go](gateway/services/telegram.go),
  [gateway/services/dispatcher.go](gateway/services/dispatcher.go),
  [gateway/services/stream.go](gateway/services/stream.go),
  [gateway/db/login_model.go](gateway/db/login_model.go),
  [gateway/Dockerfile](gateway/Dockerfile).
- Agent: [agent/app/main.py](agent/app/main.py),
  [agent/app/graph.py](agent/app/graph.py),
  [agent/app/llm.py](agent/app/llm.py),
  [agent/app/tools.py](agent/app/tools.py),
  [agent/app/db.py](agent/app/db.py),
  [agent/app/stream.py](agent/app/stream.py),
  [agent/app/envelope.py](agent/app/envelope.py),
  [agent/app/memory/short_term.py](agent/app/memory/short_term.py),
  [agent/app/memory/long_term.py](agent/app/memory/long_term.py),
  [agent/app/memory/models.py](agent/app/memory/models.py),
  [agent/config.ini](agent/config.ini),
  [agent/pyproject.toml](agent/pyproject.toml),
  [agent/Dockerfile](agent/Dockerfile).
- Contracts: [proto/cli.proto](proto/cli.proto),
  [proto/envelope.proto](proto/envelope.proto),
  [proto/generated/](proto/generated/).
- CLI client: [roamind-cli/main.go](roamind-cli/main.go).
- Ops: [process-compose.yaml](process-compose.yaml),
  [build.sh](build.sh),
  [deploy/README.md](deploy/README.md).

## Further considerations

1. **Prompt injection from channel content.** All message bodies are
   treated as untrusted data. System prompt explicitly sandboxes
   channel text.
2. **Fact contradictions.** New facts ("I'm vegetarian now") may
   contradict old ones. Today the typed `UserProfile` is overwritten
   on `upsert`; once vector memory lands, both versions will be kept
   with timestamps and the per-category summary will prefer newer.
3. **Tool error surface.** Predictable upstream failures (network
   error, 4xx/5xx, bad JSON) are converted to short strings inside
   each tool so a single flaky API does not crash the turn. Only
   unexpected exceptions reach `compact_err`.
4. **Embedding model (when vector lands).** Start with OpenAI
   `text-embedding-3-small`; keep the embedder interface abstracted so
   swapping to `bge-small` is mechanical.
5. **CLI TLS for ACA.** The Azure ingress terminates TLS at port 443;
   `roamind-cli` must dial over TLS before remote use. Currently
   blocks step 5 of the deployment runbook.
6. **Browser-tool blast radius (when MCP lands).** Sandboxed
   container, ephemeral profile, constrained network egress, no host
   mounts.

## TODOs

- **Agent and Gateway streaming support (v2).** `CliService.Query()`
  is request-response today. For agents that emit multiple messages
  per query, implement streaming so the client receives partial
  results. Agent side: emit multiple `TaskOut` envelopes with a
  `done` marker. Gateway side: keep the pending entry alive until
  end-of-stream, loop in the `Query` handler.
