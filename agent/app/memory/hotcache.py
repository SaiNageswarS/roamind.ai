"""Short-term hot cache backed by a Redis LIST per user.

Key: `chat:hot:{user_id}`. Each list element is one **turn-group**
serialized as JSON (a list of message dicts). Storing whole turns —
rather than individual messages — gives us atomic eviction: we never
end up with an orphaned `assistant` reply whose matching `user` message
got popped.

Budget: configurable `max_tokens`. After every push we trim from the
left (oldest) until the running total fits.

On a miss we rehydrate from Mongo via `MessageStore.recent_turns()`.
"""

from __future__ import annotations

import json
from typing import Optional

import redis
import structlog

from .messages import MessageStore
from .models import MessageDoc, Turn

log = structlog.get_logger("roamind.agent.memory.hotcache")

KEY_PREFIX = "chat:hot:"


class HotCache:
    """Redis-backed token-budgeted short-term memory."""

    def __init__(
        self,
        redis_client: redis.Redis,
        messages: MessageStore,
        *,
        max_tokens: int = 4000,
        rehydrate_max_turns: int = 20,
    ):
        self._redis = redis_client
        self._messages = messages
        self._max_tokens = max_tokens
        self._rehydrate_max_turns = rehydrate_max_turns

    # --- Public API -----------------------------------------------------

    def load(self, user_id: str) -> list[Turn]:
        """Return cached turns (oldest-first), rehydrating from Mongo on miss."""
        if not user_id:
            return []
        key = _key(user_id)
        raw_entries = self._redis.lrange(key, 0, -1) or []
        if raw_entries:
            return _decode_turns(raw_entries)

        # Cache miss: rehydrate from durable store.
        turns = self._messages.recent_turns(
            user_id, max_turns=self._rehydrate_max_turns
        )
        if turns:
            self._reseed(key, turns)
        return turns

    def append_turn(self, user_id: str, turn: Turn) -> None:
        """Push a completed turn-group and trim to the token budget."""
        if not user_id or not turn.messages:
            return
        key = _key(user_id)
        self._redis.rpush(key, _encode_turn(turn))
        self._trim_to_budget(key)

    # --- Private helpers ------------------------------------------------

    def _reseed(self, key: str, turns: list[Turn]) -> None:
        pipe = self._redis.pipeline()
        pipe.delete(key)
        for t in turns:
            pipe.rpush(key, _encode_turn(t))
        pipe.execute()
        self._trim_to_budget(key)

    def _trim_to_budget(self, key: str) -> None:
        """Pop oldest turn-groups until the total token count fits.

        Implemented in two passes (compute → trim) to keep the logic
        readable; the cache is small per user so this is cheap.
        """
        entries = self._redis.lrange(key, 0, -1) or []
        if not entries:
            return
        turns = _decode_turns(entries)
        total = sum(t.total_tokens for t in turns)
        drop = 0
        while total > self._max_tokens and drop < len(turns) - 1:
            total -= turns[drop].total_tokens
            drop += 1
        if drop:
            # LTRIM keeps [drop, -1]; equivalent to popping `drop` from the left.
            self._redis.ltrim(key, drop, -1)


# --- Module-level helpers -----------------------------------------------


def _key(user_id: str) -> str:
    return f"{KEY_PREFIX}{user_id}"


def _encode_turn(turn: Turn) -> str:
    return json.dumps(
        {
            "turn_id": turn.turn_id,
            "messages": [m.model_dump(mode="json") for m in turn.messages],
        },
        separators=(",", ":"),
    )


def _decode_turns(entries: list[str]) -> list[Turn]:
    out: list[Turn] = []
    for raw in entries:
        try:
            obj = json.loads(raw)
            msgs = [MessageDoc.model_validate(m) for m in obj.get("messages", [])]
            out.append(Turn(turn_id=obj.get("turn_id", ""), messages=msgs))
        except Exception as e:  # pragma: no cover — defensive
            log.warning("hotcache decode failed; skipping entry", err=str(e))
    return out
