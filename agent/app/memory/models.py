"""Pydantic models for the memory subsystem.

Two clearly separated tiers:
  • short-term  → `ChatMessage` (Redis-only)
  • long-term   → `UserProfile`, `KnowledgeDoc`, `Event` (Mongo)

Each long-term type lives in its own collection and is accessed by
tools at the `act` node — not eagerly loaded at intake.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

from pydantic import BaseModel, ConfigDict, Field


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


# --- Short-term ---------------------------------------------------------


class ChatMessage(BaseModel):
    """One message in the short-term conversation history."""

    model_config = ConfigDict(extra="allow")

    role: str  # "user" | "assistant" | "tool"
    content: str
    channel: str = ""
    created_at: datetime = Field(default_factory=_utcnow)


# --- Long-term ----------------------------------------------------------


class UserProfile(BaseModel):
    """Free-form per-user profile. Updated by tools as the conversation
    surfaces new facts (name, timezone, profession, preferences, etc.).
    """

    model_config = ConfigDict(extra="allow")

    user_id: str
    data: dict[str, Any] = Field(default_factory=dict)
    updated_at: datetime = Field(default_factory=_utcnow)


class KnowledgeDoc(BaseModel):
    """A document in the per-user knowledge base.

    `category` partitions the KB (e.g. "health", "project.roamind",
    "travel"). `content` is plain text / markdown. Searched via Mongo
    `$text` over (title, content) and filtered by `user_id` + optional
    `category`.
    """

    model_config = ConfigDict(extra="allow")

    id: str
    user_id: str
    category: str
    title: str = ""
    content: str = ""
    tags: list[str] = Field(default_factory=list)
    created_at: datetime = Field(default_factory=_utcnow)
    updated_at: datetime = Field(default_factory=_utcnow)


class Event(BaseModel):
    """A structured time-stamped event.

    `kind` is a free-form label (e.g. "workout", "task_completed").
    `payload` is kind-specific so adding new tracking dimensions never
    requires a schema migration.
    """

    model_config = ConfigDict(extra="allow")

    id: str
    user_id: str
    kind: str
    payload: dict[str, Any] = Field(default_factory=dict)
    occurred_at: datetime = Field(default_factory=_utcnow)
