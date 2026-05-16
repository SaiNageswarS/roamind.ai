"""Pydantic models for memory documents.

Mirrors the Mongo schema. Field names use snake_case (Mongo) — wire
serialization to the LLM/graph uses LangChain `BaseMessage` directly,
not these models.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Literal, Optional

from pydantic import BaseModel, ConfigDict, Field


Role = Literal["user", "assistant", "tool", "system"]


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class CoreMemory(BaseModel):
    """Per-user always-injected context (preferences, timezone, identity)."""

    model_config = ConfigDict(extra="allow")

    user_id: str
    # Free-form key/value bag. Kept small (rendered into the system prompt).
    data: dict[str, Any] = Field(default_factory=dict)
    updated_at: datetime = Field(default_factory=_utcnow)


class MessageDoc(BaseModel):
    """One message in the transcript.

    A `turn_id` groups messages that belong to the same user→assistant
    exchange (user msg, any tool messages, assistant reply). Hot-cache
    eviction operates on whole turns so AI replies never become orphans.
    """

    model_config = ConfigDict(extra="allow")

    id: str
    user_id: str
    turn_id: str
    channel: str = ""
    role: Role
    content: str
    # Approximate token count (tiktoken). Used for hot-cache budgeting.
    token_count: int = 0
    # Optional structured payload (tool name/args for tool messages, etc).
    meta: dict[str, Any] = Field(default_factory=dict)
    created_at: datetime = Field(default_factory=_utcnow)


class Turn(BaseModel):
    """A grouped exchange. In-memory representation only (not persisted
    as a single document — `messages` collection stores per-message rows).
    """

    turn_id: str
    messages: list[MessageDoc] = Field(default_factory=list)

    @property
    def total_tokens(self) -> int:
        return sum(m.token_count for m in self.messages)


class MemoryContext(BaseModel):
    """Result of `Memory.load_context(user_id, query)`.

    Consumed by the graph's `intake` node to seed the LLM message list.
    """

    model_config = ConfigDict(arbitrary_types_allowed=True)

    core: Optional[CoreMemory] = None
    turns: list[Turn] = Field(default_factory=list)
    # Reserved for the long-term tier (filled when facts.py lands).
    facts: list[Any] = Field(default_factory=list)
    summaries: list[Any] = Field(default_factory=list)
