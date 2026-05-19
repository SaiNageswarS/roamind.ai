"""MongoDB client and index bootstrap.

Mongo holds the **long-term** memory only (per design — Redis is
exclusive for short-term conversation history). Collections owned here:

    user_profiles      one doc per user (free-form key/value)
    knowledge          per-user knowledge base, `$text`-indexed
    events             per-user time-series of structured events

If `uri` is empty / unset, `MongoDB.connect()` returns `None`. The
agent runs in Redis-only mode in that case — long-term tools become
no-ops; short-term still works.
"""

from __future__ import annotations

import os
from typing import Optional

import structlog
from pymongo import ASCENDING, DESCENDING, MongoClient
from pymongo.collection import Collection
from pymongo.database import Database
from pymongo.errors import OperationFailure

log = structlog.get_logger("roamind.agent.db")

# Mongo server error code for `IndexOptionsConflict`: an index with the
# same key spec already exists under a different name (or with different
# options). Safe to ignore — the existing index already covers the query.
_INDEX_OPTIONS_CONFLICT = 85
_INDEX_KEY_SPECS_CONFLICT = 86  # same name, different keys — also tolerate.


class MongoDB:
    """Thin wrapper around a `MongoClient` + named collections."""

    def __init__(self, client: MongoClient, db_name: str):
        self.client = client
        self.db: Database = client[db_name]
        self.user_profiles: Collection = self.db["user_profiles"]
        self.knowledge: Collection = self.db["knowledge"]
        self.events: Collection = self.db["events"]
        # Habit tracking (gateway is the writer; agent reads only).
        self.habits: Collection = self.db["habits"]
        self.habit_entries: Collection = self.db["habit_entries"]
        self.habit_weekly: Collection = self.db["habit_weekly"]
        self.habit_monthly: Collection = self.db["habit_monthly"]

    # --- Public API -----------------------------------------------------

    @classmethod
    def connect(cls, *, uri: str | None, db_name: str) -> Optional["MongoDB"]:
        """Connect to Mongo if a URI is provided; otherwise return None."""
        uri = uri or os.getenv("MONGO_URI") or ""
        if not uri.strip():
            log.warning("mongo disabled: no uri configured")
            return None

        client: MongoClient = MongoClient(uri, serverSelectionTimeoutMS=5000)
        client.admin.command("ping")
        inst = cls(client, db_name)
        inst.ensure_indexes()
        log.info("mongo connected", db=db_name)
        return inst

    def ensure_indexes(self) -> None:
        """Idempotent index assertions.

        Tolerates `IndexOptionsConflict` (code 85): when another writer
        (e.g. the Go gateway) has already created an equivalent index
        under a different name, we keep that index as-is rather than
        failing startup.
        """
        self._create_index(
            self.user_profiles, [("user_id", ASCENDING)], unique=True
        )

        self._create_index(
            self.knowledge,
            [("user_id", ASCENDING), ("id", ASCENDING)],
            unique=True,
        )
        self._create_index(
            self.knowledge, [("user_id", ASCENDING), ("category", ASCENDING)]
        )
        # Full-text search index used by `LongTermMemory.knowledge.search()`.
        # Mongo allows only one $text index per collection.
        self._create_index(
            self.knowledge,
            [("title", "text"), ("content", "text")],
            name="knowledge_text",
        )

        self._create_index(
            self.events,
            [("user_id", ASCENDING), ("occurred_at", DESCENDING)],
        )
        self._create_index(
            self.events,
            [("user_id", ASCENDING), ("kind", ASCENDING), ("occurred_at", DESCENDING)],
        )

        # Habit indexes mirror the gateway's `EnsureHabitIndexes`. We use
        # the exact same names so repeated startups don't conflict.
        self._create_index(
            self.habits,
            [("user_id", ASCENDING), ("slug", ASCENDING)],
            unique=True,
            name="uniq_user_slug",
        )
        self._create_index(
            self.habit_entries,
            [("user_id", ASCENDING), ("habit_id", ASCENDING), ("date", ASCENDING)],
            unique=True,
            name="uniq_user_habit_date",
        )
        self._create_index(
            self.habit_entries,
            [("user_id", ASCENDING), ("date", ASCENDING)],
            name="user_date",
        )
        self._create_index(
            self.habit_weekly,
            [("user_id", ASCENDING), ("habit_id", ASCENDING),
             ("iso_year", ASCENDING), ("iso_week", ASCENDING)],
            unique=True,
            name="uniq_user_habit_week",
        )
        self._create_index(
            self.habit_monthly,
            [("user_id", ASCENDING), ("habit_id", ASCENDING),
             ("year", ASCENDING), ("month", ASCENDING)],
            unique=True,
            name="uniq_user_habit_month",
        )

    def close(self) -> None:
        self.client.close()

    # --- Private helpers -----------------------------------------------

    @staticmethod
    def _create_index(collection: Collection, keys, **kwargs) -> None:
        try:
            collection.create_index(keys, **kwargs)
        except OperationFailure as exc:
            if exc.code in (_INDEX_OPTIONS_CONFLICT, _INDEX_KEY_SPECS_CONFLICT):
                log.warning(
                    "index already exists with conflicting options; keeping existing",
                    collection=collection.name,
                    keys=keys,
                    code=exc.code,
                    err=str(exc),
                )
                return
            raise
