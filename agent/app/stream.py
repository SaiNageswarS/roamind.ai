"""Redis Streams helpers for the agent.

Mirrors `gateway/services/stream.go` so the two ends agree on stream
names, consumer groups, and the wire-format payload field.
"""

from __future__ import annotations

import os
from typing import Iterable

import redis

STREAM_TASKS_IN = "tasks.in"
STREAM_TASKS_OUT = "tasks.out"
STREAM_TASKS_DLQ = "tasks.dlq"

GROUP_AGENT = "agent"
CONSUMER_AGENT = "agent-1"

PAYLOAD_FIELD = "payload"


def new_redis_client() -> redis.Redis:
    """Create a sync Redis client from REDIS_URL (default localhost:6379)."""
    url = os.getenv("REDIS_URL", "redis://localhost:6379")
    client = redis.Redis.from_url(url, decode_responses=True)
    client.ping()
    return client


def ensure_group(client: redis.Redis, stream: str, group: str) -> None:
    """Idempotent XGROUP CREATE MKSTREAM."""
    try:
        client.xgroup_create(stream, group, id="$", mkstream=True)
    except redis.ResponseError as e:
        if "BUSYGROUP" not in str(e):
            raise


def xreadgroup_tasks_in(
    client: redis.Redis,
    *,
    count: int = 16,
    block_ms: int = 2000,
) -> list:
    """Block-read pending messages for this consumer."""
    return client.xreadgroup(
        groupname=GROUP_AGENT,
        consumername=CONSUMER_AGENT,
        streams={STREAM_TASKS_IN: ">"},
        count=count,
        block=block_ms,
    ) or []


def xack_tasks_in(client: redis.Redis, ids: Iterable[str]) -> None:
    ids = list(ids)
    if ids:
        client.xack(STREAM_TASKS_IN, GROUP_AGENT, *ids)


def xadd_tasks_out(client: redis.Redis, payload_json: str) -> str:
    return client.xadd(STREAM_TASKS_OUT, {PAYLOAD_FIELD: payload_json})


def xadd_dlq(client: redis.Redis, payload_json: str, original_id: str) -> str:
    return client.xadd(
        STREAM_TASKS_DLQ,
        {PAYLOAD_FIELD: payload_json, "original_id": original_id},
    )


def pending_retry_count(client: redis.Redis, msg_id: str) -> int:
    pending = client.xpending_range(
        STREAM_TASKS_IN,
        GROUP_AGENT,
        min=msg_id,
        max=msg_id,
        count=1,
    )
    if not pending:
        return 0
    # redis-py returns dicts with 'times_delivered'
    entry = pending[0]
    return int(entry.get("times_delivered", 0))
