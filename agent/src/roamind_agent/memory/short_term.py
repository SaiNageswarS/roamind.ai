"""Short-term memory: last N messages per user, in Redis.

Exclusive store for recent conversation history. No Mongo persistence,
no rehydrate — if Redis loses the key, the recent history is gone (use
Redis AOF/RDB for durability).

Key: `chat:hot:{user_id}` — a Redis LIST of JSON-encoded `ChatMessage`s,
oldest at index 0. `LTRIM` keeps only the last `max_messages` entries.
"""

from __future__ import annotations

import json

import redis
import structlog

from .models import ChatMessage

log = structlog.get_logger("roamind.agent.memory.short_term")

KEY_PREFIX = "chat:hot:"


class ShortTermMemory:
    """Per-user rolling window of recent messages."""

    def __init__(self, redis_client: redis.Redis, *, max_messages: int = 8):
        self._redis = redis_client
        self._max = max_messages

    # --- Public API -----------------------------------------------------

    def load(self, user_id: str) -> list[ChatMessage]:
        if not user_id:
            return []
        raw = self._redis.lrange(_key(user_id), 0, -1) or []
        out: list[ChatMessage] = []
        for entry in raw:
            try:
                out.append(ChatMessage.model_validate_json(entry))
            except Exception as e:  # pragma: no cover — defensive
                log.warning("short_term decode failed", err=str(e))
        return out

    def append(self, user_id: str, message: ChatMessage) -> None:
        if not user_id:
            return
        key = _key(user_id)
        pipe = self._redis.pipeline()
        pipe.rpush(key, message.model_dump_json())
        pipe.ltrim(key, -self._max, -1)
        pipe.execute()

    def clear(self, user_id: str) -> None:
        if user_id:
            self._redis.delete(_key(user_id))


# --- Module-level helpers -----------------------------------------------


def _key(user_id: str) -> str:
    return f"{KEY_PREFIX}{user_id}"
