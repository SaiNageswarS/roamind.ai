"""Memory subsystem — single public surface for the agent graph.

The graph imports only this module:

    from .memory import Memory, MemoryContext

`Memory` composes the three v1 tiers (core, transcript, hot cache) and
will grow to wrap the long-term tier (facts + summaries) when Atlas
Vector Search is wired in.

Usage from the graph:

    ctx = memory.load_context(user_id, query)
    # ... LLM call ...
    memory.record_turn(
        user_id=user_id,
        channel=channel,
        user_text=task_in.text,
        assistant_text=ai_msg.content,
    )
"""

from __future__ import annotations

import configparser
import uuid
from typing import Optional

import redis
import structlog
from langchain_core.messages import AIMessage, BaseMessage, HumanMessage, ToolMessage

from ..db import MongoDB
from .core import CoreMemoryStore
from .hotcache import HotCache
from .messages import MessageStore
from .models import CoreMemory, MemoryContext, MessageDoc, Turn

__all__ = [
    "Memory",
    "MemoryContext",
    "CoreMemory",
    "Turn",
    "MessageDoc",
]

log = structlog.get_logger("roamind.agent.memory")


class Memory:
    """Facade over core memory, durable transcript, and Redis hot cache."""

    def __init__(
        self,
        *,
        mongo: Optional[MongoDB],
        redis_client: redis.Redis,
        max_tokens: int = 4000,
        token_encoding: str = "cl100k_base",
        rehydrate_max_turns: int = 20,
    ):
        self._core = CoreMemoryStore(mongo)
        self._messages = MessageStore(mongo, token_encoding=token_encoding)
        self._hot = HotCache(
            redis_client,
            self._messages,
            max_tokens=max_tokens,
            rehydrate_max_turns=rehydrate_max_turns,
        )

    # --- Construction --------------------------------------------------

    @classmethod
    def from_config(
        cls,
        cfg: configparser.ConfigParser,
        *,
        mongo: Optional[MongoDB],
        redis_client: redis.Redis,
    ) -> "Memory":
        return cls(
            mongo=mongo,
            redis_client=redis_client,
            max_tokens=cfg.getint("memory", "hot_cache_max_tokens", fallback=4000),
            token_encoding=cfg.get("memory", "token_encoding", fallback="cl100k_base"),
            rehydrate_max_turns=cfg.getint("memory", "rehydrate_max_turns", fallback=20),
        )

    # --- Public API -----------------------------------------------------

    def load_context(self, user_id: str, _query: str = "") -> MemoryContext:
        """Assemble the memory context to seed the LLM message list.

        `_query` is reserved for the long-term tier (vector recall over
        facts). It is unused in v1 and accepted so callers can pass it
        unconditionally without a later API churn.
        """
        return MemoryContext(
            core=self._core.get(user_id),
            turns=self._hot.load(user_id),
        )

    def render_core_for_prompt(self, mem: Optional[CoreMemory]) -> str:
        return self._core.render_for_prompt(mem)

    def turns_to_messages(self, turns: list[Turn]) -> list[BaseMessage]:
        """Replay cached turns as LangChain messages for the LLM.

        Channel switches are surfaced with a `[via {channel}]` prefix on
        user messages (per plan.md short-term tier).
        """
        out: list[BaseMessage] = []
        for turn in turns:
            for m in turn.messages:
                out.append(_to_lc_message(m))
        return out

    def record_turn(
        self,
        *,
        user_id: str,
        channel: str,
        user_text: str,
        assistant_text: str,
        turn_id: str | None = None,
    ) -> str:
        """Persist a completed user→assistant exchange and update hot cache.

        Returns the `turn_id` so callers can correlate (e.g. enqueue a
        memorize event keyed by the same id later).
        """
        tid = turn_id or str(uuid.uuid4())
        user_msg = self._messages.append(
            user_id=user_id,
            turn_id=tid,
            role="user",
            content=user_text,
            channel=channel,
        )
        asst_msg = self._messages.append(
            user_id=user_id,
            turn_id=tid,
            role="assistant",
            content=assistant_text,
            channel=channel,
        )
        self._hot.append_turn(
            user_id,
            Turn(turn_id=tid, messages=[user_msg, asst_msg]),
        )
        return tid

    def update_core(self, user_id: str, data: dict) -> Optional[CoreMemory]:
        return self._core.upsert(user_id, data)


# --- Module-level helpers -----------------------------------------------


def _to_lc_message(m: MessageDoc) -> BaseMessage:
    """Map a persisted `MessageDoc` to a LangChain `BaseMessage`."""
    content = m.content
    if m.role == "user":
        if m.channel:
            content = f"[via {m.channel}] {content}"
        return HumanMessage(content=content)
    if m.role == "assistant":
        return AIMessage(content=content)
    if m.role == "tool":
        return ToolMessage(
            content=content,
            tool_call_id=m.meta.get("tool_call_id", ""),
        )
    # `system` and any unknown role fall through as HumanMessage so the
    # LLM still sees the content; system prompts are rebuilt every turn
    # by the graph and should not be persisted in `messages`.
    return HumanMessage(content=content)
