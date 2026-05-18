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

log = structlog.get_logger("roamind.agent.db")


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
        """Idempotent index assertions."""
        self.user_profiles.create_index([("user_id", ASCENDING)], unique=True)

        self.knowledge.create_index(
            [("user_id", ASCENDING), ("id", ASCENDING)],
            unique=True,
        )
        self.knowledge.create_index([("user_id", ASCENDING), ("category", ASCENDING)])
        # Full-text search index used by `LongTermMemory.knowledge.search()`.
        # Mongo allows only one $text index per collection.
        self.knowledge.create_index(
            [("title", "text"), ("content", "text")],
            name="knowledge_text",
        )

        self.events.create_index(
            [("user_id", ASCENDING), ("occurred_at", DESCENDING)],
        )
        self.events.create_index(
            [("user_id", ASCENDING), ("kind", ASCENDING), ("occurred_at", DESCENDING)],
        )

        # Habit indexes mirror the gateway's `EnsureHabitIndexes`; safe to
        # assert from both sides because they are pure CreateIndex calls.
        self.habits.create_index(
            [("user_id", ASCENDING), ("slug", ASCENDING)], unique=True
        )
        self.habit_entries.create_index(
            [("user_id", ASCENDING), ("habit_id", ASCENDING), ("date", ASCENDING)],
            unique=True,
        )
        self.habit_entries.create_index(
            [("user_id", ASCENDING), ("date", ASCENDING)],
        )
        self.habit_weekly.create_index(
            [("user_id", ASCENDING), ("habit_id", ASCENDING),
             ("iso_year", ASCENDING), ("iso_week", ASCENDING)],
            unique=True,
        )
        self.habit_monthly.create_index(
            [("user_id", ASCENDING), ("habit_id", ASCENDING),
             ("year", ASCENDING), ("month", ASCENDING)],
            unique=True,
        )

    def close(self) -> None:
        self.client.close()
