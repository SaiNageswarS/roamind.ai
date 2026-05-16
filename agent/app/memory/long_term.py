"""Long-term memory: Mongo-backed CRUD across three collections.

Three sub-stores exposed on `LongTermMemory`:

  .profile      one doc per user (free-form key/value)
  .knowledge    per-user knowledge base, Mongo `$text` search
  .events       per-user time-series of structured events

Tools in the `act` node use these stores; the graph itself does not
read or render long-term memory. New domains (e.g. "health.records",
"tasks") are added by partitioning `knowledge.category` rather than
introducing new collections.
"""

from __future__ import annotations

import uuid
from datetime import datetime, timedelta, timezone
from typing import Any, Optional

import structlog

from ..db import MongoDB
from .models import Event, KnowledgeDoc, UserProfile

log = structlog.get_logger("roamind.agent.memory.long_term")


class LongTermMemory:
    """Composite of `.profile`, `.knowledge`, `.events`."""

    def __init__(self, mongo: Optional[MongoDB]):
        self.profile = _ProfileStore(mongo)
        self.knowledge = _KnowledgeStore(mongo)
        self.events = _EventStore(mongo)


class _ProfileStore:
    """Free-form per-user profile."""

    def __init__(self, mongo: Optional[MongoDB]):
        self._mongo = mongo

    # --- Public API -----------------------------------------------------

    def get(self, user_id: str) -> Optional[UserProfile]:
        if self._mongo is None or not user_id:
            return None
        raw = self._mongo.user_profiles.find_one({"user_id": user_id})
        if raw is None:
            return None
        raw.pop("_id", None)
        return UserProfile.model_validate(raw)

    def upsert(self, user_id: str, fields: dict[str, Any]) -> Optional[UserProfile]:
        """Shallow-merge `fields` into the user's profile."""
        if self._mongo is None or not user_id:
            return None
        existing = self.get(user_id)
        merged = {**(existing.data if existing else {}), **fields}
        now = datetime.now(timezone.utc)
        self._mongo.user_profiles.update_one(
            {"user_id": user_id},
            {"$set": {"user_id": user_id, "data": merged, "updated_at": now}},
            upsert=True,
        )
        return UserProfile(user_id=user_id, data=merged, updated_at=now)


class _KnowledgeStore:
    """Per-user knowledge base with Mongo `$text` search."""

    def __init__(self, mongo: Optional[MongoDB]):
        self._mongo = mongo

    # --- Public API -----------------------------------------------------

    def get(self, user_id: str, doc_id: str) -> Optional[KnowledgeDoc]:
        if self._mongo is None or not user_id:
            return None
        raw = self._mongo.knowledge.find_one({"user_id": user_id, "id": doc_id})
        if raw is None:
            return None
        raw.pop("_id", None)
        return KnowledgeDoc.model_validate(raw)

    def list_by_category(self, user_id: str, category: str) -> list[KnowledgeDoc]:
        if self._mongo is None or not user_id:
            return []
        cursor = self._mongo.knowledge.find(
            {"user_id": user_id, "category": category}
        ).sort("updated_at", -1)
        return [_to_doc(raw) for raw in cursor]

    def search(
        self,
        user_id: str,
        query: str,
        *,
        category: str | None = None,
        limit: int = 5,
    ) -> list[KnowledgeDoc]:
        if self._mongo is None or not user_id or not query.strip():
            return []
        q: dict[str, Any] = {"user_id": user_id, "$text": {"$search": query}}
        if category:
            q["category"] = category
        cursor = (
            self._mongo.knowledge.find(q, {"score": {"$meta": "textScore"}})
            .sort([("score", {"$meta": "textScore"})])
            .limit(limit)
        )
        return [_to_doc(raw) for raw in cursor]

    def upsert(
        self,
        *,
        user_id: str,
        category: str,
        title: str,
        content: str,
        tags: list[str] | None = None,
        doc_id: str | None = None,
    ) -> Optional[KnowledgeDoc]:
        if self._mongo is None or not user_id:
            return None
        now = datetime.now(timezone.utc)
        doc_id = doc_id or str(uuid.uuid4())
        doc = KnowledgeDoc(
            id=doc_id,
            user_id=user_id,
            category=category,
            title=title,
            content=content,
            tags=tags or [],
            updated_at=now,
        )
        self._mongo.knowledge.update_one(
            {"user_id": user_id, "id": doc_id},
            {
                "$set": doc.model_dump(mode="json"),
                "$setOnInsert": {"created_at": now.isoformat()},
            },
            upsert=True,
        )
        return doc

    def delete(self, user_id: str, doc_id: str) -> bool:
        if self._mongo is None or not user_id:
            return False
        res = self._mongo.knowledge.delete_one({"user_id": user_id, "id": doc_id})
        return res.deleted_count > 0


class _EventStore:
    """Per-user time-series events."""

    def __init__(self, mongo: Optional[MongoDB]):
        self._mongo = mongo

    # --- Public API -----------------------------------------------------

    def log(
        self,
        *,
        user_id: str,
        kind: str,
        payload: dict[str, Any] | None = None,
        occurred_at: datetime | None = None,
    ) -> Optional[Event]:
        if self._mongo is None or not user_id:
            return None
        evt = Event(
            id=str(uuid.uuid4()),
            user_id=user_id,
            kind=kind,
            payload=payload or {},
            occurred_at=occurred_at or datetime.now(timezone.utc),
        )
        self._mongo.events.insert_one(evt.model_dump(mode="json"))
        return evt

    def query(
        self,
        user_id: str,
        *,
        kind: str | None = None,
        since_days: int | None = None,
        limit: int = 50,
    ) -> list[Event]:
        if self._mongo is None or not user_id:
            return []
        q: dict[str, Any] = {"user_id": user_id}
        if kind:
            q["kind"] = kind
        if since_days is not None:
            since = datetime.now(timezone.utc) - timedelta(days=since_days)
            q["occurred_at"] = {"$gte": since.isoformat()}
        cursor = self._mongo.events.find(q).sort("occurred_at", -1).limit(limit)
        return [_to_event(raw) for raw in cursor]

    def count_by_kind(
        self,
        user_id: str,
        *,
        since_days: int,
    ) -> dict[str, int]:
        if self._mongo is None or not user_id or since_days <= 0:
            return {}
        since = datetime.now(timezone.utc) - timedelta(days=since_days)
        pipeline = [
            {
                "$match": {
                    "user_id": user_id,
                    "occurred_at": {"$gte": since.isoformat()},
                }
            },
            {"$group": {"_id": "$kind", "n": {"$sum": 1}}},
        ]
        return {row["_id"]: int(row["n"]) for row in self._mongo.events.aggregate(pipeline)}


# --- Module-level helpers -----------------------------------------------


def _to_doc(raw: dict) -> KnowledgeDoc:
    raw.pop("_id", None)
    raw.pop("score", None)
    return KnowledgeDoc.model_validate(raw)


def _to_event(raw: dict) -> Event:
    raw.pop("_id", None)
    return Event.model_validate(raw)
