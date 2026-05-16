"""Pydantic models for the memory subsystem.

Short-term:  `ChatMessage` (Redis-only).
Long-term:   `UserProfile`, `KnowledgeDoc`, `Event` (Mongo).
"""

from __future__ import annotations

from datetime import date, datetime, timezone
from typing import Any, Optional

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


# --- Long-term: profile sub-records ------------------------------------


class Education(BaseModel):
    """One education entry on a user's profile."""

    model_config = ConfigDict(extra="allow")

    degree: str = ""
    field: str = ""
    institution: str = ""
    year: Optional[int] = None  # graduation year


class WorkExperience(BaseModel):
    """One work-experience entry on a user's profile."""

    model_config = ConfigDict(extra="allow")

    role: str = ""
    company: str = ""
    start_year: Optional[int] = None
    end_year: Optional[int] = None  # None == current


# --- Long-term: top-level documents ------------------------------------


class UserProfile(BaseModel):
    """Per-user identity profile, injected into the system prompt every turn.

    Typed fields cover the common, stable facts a personal assistant
    benefits from knowing on every call. Anything else the assistant
    learns can be stashed in `extras` without a schema change.
    """

    model_config = ConfigDict(extra="allow")

    user_id: str

    # Identity
    name: Optional[str] = None
    gender: Optional[str] = None
    date_of_birth: Optional[date] = None
    nationality: Optional[str] = None
    ethnicity: Optional[str] = None

    # Location
    location: Optional[str] = None

    # Career
    profession: Optional[str] = None
    employer: Optional[str] = None
    work_experience: list[WorkExperience] = Field(default_factory=list)
    education: list[Education] = Field(default_factory=list)

    # Free-form extension bag for anything that doesn't fit above.
    extras: dict[str, Any] = Field(default_factory=dict)

    updated_at: datetime = Field(default_factory=_utcnow)

    # --- Public API: rendering -----------------------------------------

    def render_for_prompt(self) -> str:
        """Compact human-readable block for the system prompt.

        Skips empty fields. Returns "" if the profile has nothing to say
        — callers should omit the block in that case.
        """
        lines: list[str] = []

        # Identity block
        if self.name:
            lines.append(f"Name: {self.name}")
        if self.gender:
            lines.append(f"Gender: {self.gender}")
        if self.date_of_birth:
            lines.append(f"Date of birth: {self.date_of_birth.isoformat()}")
        if self.nationality:
            lines.append(f"Nationality: {self.nationality}")
        if self.ethnicity:
            lines.append(f"Ethnicity: {self.ethnicity}")

        # Location block
        if self.location:
            lines.append(f"Location: {self.location}")

        # Career block
        if self.profession or self.employer:
            career = self.profession or ""
            if self.employer:
                career = f"{career} at {self.employer}" if career else self.employer
            lines.append(f"Profession: {career}")

        for w in self.work_experience:
            if not (w.role or w.company):
                continue
            span = _format_years(w.start_year, w.end_year)
            head = " at ".join(b for b in (w.role, w.company) if b)
            lines.append(f"- {head}{(' ' + span) if span else ''}")

        if self.education:
            for e in self.education:
                if not (e.degree or e.institution):
                    continue
                bits = [b for b in (e.degree, e.field) if b]
                head = ", ".join(bits) or "Education"
                tail = e.institution
                year = f" ({e.year})" if e.year else ""
                lines.append(f"- {head}" + (f" — {tail}" if tail else "") + year)

        # Extras
        for k, v in self.extras.items():
            lines.append(f"{k}: {v}")

        if not lines:
            return ""
        return "Known about the user:\n" + "\n".join(lines)


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


# --- Module-level helpers -----------------------------------------------


def _format_years(start: Optional[int], end: Optional[int]) -> str:
    if start is None and end is None:
        return ""
    if start is not None and end is None:
        return f"({start}–present)"
    if start is None and end is not None:
        return f"(–{end})"
    return f"({start}–{end})"
