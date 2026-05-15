# Roamind Agent (Python + LangGraph)

Per [`../plan.md`](../plan.md) Phase 3. Consumes `tasks.in` from Redis,
runs each envelope through a LangGraph, and emits `tasks.out`.

## Layout

```
agent/
├── pyproject.toml
└── app/
    ├── __init__.py
    ├── main.py        # worker loop (XREADGROUP / XACK / DLQ)
    ├── envelope.py    # pydantic models for TaskIn / TaskOut
    ├── stream.py      # Redis stream helpers
    └── graph.py       # LangGraph: intake → plan → act → compact_err → respond
```

Planned subpackages (to be added in Phase 3):
`app/llm/`, `app/memory/`, `app/tools/`, `app/mcp_clients.py`,
`app/checkpoint_mongo.py`.

## Install

```sh
cd agent
python -m venv .venv && source .venv/bin/activate
pip install -e .
```

For LLM/MCP/Mongo support install extras as needed:

```sh
pip install -e '.[openai,anthropic,ollama,mongo,mcp]'
```

## Run

```sh
REDIS_URL=redis://localhost:6379 python -m app.main
# or, after install:
roamind-agent
```

## v1 behaviour

The graph currently echoes the user's text back. This validates the
end-to-end wiring (CLI → gateway → Redis → agent → Redis → gateway →
CLI) before plugging in real LLM planning and tool calls.
