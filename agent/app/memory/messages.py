"""Append-only message transcript backed by Mongo `messages` collection.

This is the **durable** store. Hot-cache is a separate concern (see
`hotcache.py`). On cache miss the hot-cache rehydrates by calling
`recent_turns()` here.

Token counting uses `tiktoken` with a configurable encoding (defaults
to `cl100k_base`, a reasonable approximation across both OpenAI and
Anthropic models — Anthropic does not publish a public tokenizer).
"""

from __future__ import annotations

import uuid
from typing import Optional

import structlog

from ..db import MongoDB
from .models import MessageDoc, Role, Turn

log = structlog.get_logger("roamind.agent.memory.messages")


class MessageStore:
    """Per-user append-only transcript with turn-grouped reads."""

    def __init__(self, mongo: Optional[MongoDB], *, token_encoding: str = "cl100k_base"):
        self._mongo = mongo
        self._encoding_name = token_encoding
        self._encoder = _load_encoder(token_encoding)

    # --- Public API -----------------------------------------------------

    def count_tokens(self, text: str) -> int:
        """Approximate token count using the configured tiktoken encoding."""
        if not text:
            return 0
        if self._encoder is None:
            # Fallback: rough 1 token ≈ 4 chars heuristic.
            return max(1, len(text) // 4)
        return len(self._encoder.encode(text))

    def append(
        self,
        *,
        user_id: str,
        turn_id: str,
        role: Role,
        content: str,
        channel: str = "",
        meta: dict | None = None,
    ) -> MessageDoc:
        """Append a single message and return the persisted doc.

        Persistence is a no-op when Mongo is disabled; the returned doc
        is still valid (with a generated id) so callers can route it to
        the hot cache uniformly.
        """
        doc = MessageDoc(
            id=str(uuid.uuid4()),
            user_id=user_id,
            turn_id=turn_id,
            channel=channel,
            role=role,
            content=content,
            token_count=self.count_tokens(content),
            meta=meta or {},
        )
        if self._mongo is not None and user_id:
            self._mongo.messages.insert_one(doc.model_dump())
        return doc

    def recent_turns(self, user_id: str, *, max_turns: int) -> list[Turn]:
        """Return the most recent `max_turns` turn-groups, oldest-first.

        Used by the hot cache to rehydrate on a miss. Returns `[]` when
        Mongo is disabled.
        """
        if self._mongo is None or not user_id or max_turns <= 0:
            return []

        # Find the last `max_turns` distinct turn_ids by latest activity.
        pipeline = [
            {"$match": {"user_id": user_id}},
            {"$sort": {"created_at": -1}},
            {
                "$group": {
                    "_id": "$turn_id",
                    "last_at": {"$first": "$created_at"},
                }
            },
            {"$sort": {"last_at": -1}},
            {"$limit": max_turns},
        ]
        turn_ids = [g["_id"] for g in self._mongo.messages.aggregate(pipeline)]
        if not turn_ids:
            return []

        cursor = self._mongo.messages.find(
            {"user_id": user_id, "turn_id": {"$in": turn_ids}}
        ).sort("created_at", 1)

        by_turn: dict[str, Turn] = {tid: Turn(turn_id=tid) for tid in turn_ids}
        for raw in cursor:
            raw.pop("_id", None)
            msg = MessageDoc.model_validate(raw)
            by_turn[msg.turn_id].messages.append(msg)

        # Return oldest-first so the hot cache replays in chronological order.
        ordered = [by_turn[tid] for tid in reversed(turn_ids)]
        return ordered


# --- Module-level helpers -----------------------------------------------


def _load_encoder(name: str):
    """Best-effort tiktoken encoder load. Returns None if unavailable."""
    try:
        import tiktoken  # type: ignore[import-not-found]

        return tiktoken.get_encoding(name)
    except Exception as e:  # pragma: no cover — defensive
        log.warning("tiktoken unavailable; using char heuristic", err=str(e))
        return None
