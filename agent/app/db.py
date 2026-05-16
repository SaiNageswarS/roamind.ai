"""MongoDB client and index bootstrap.

Atlas is the durable source of truth (per plan.md). This module exposes
a single `MongoDB` facade that holds the client + collections and
asserts the indexes the rest of the codebase depends on.

Collections owned here (v1 subset):
    - core_memory          per-user always-injected facts
    - messages             append-only transcript
    - reminders            (future) scheduled prompts
    - conversation_state   (future) LangGraph checkpoints
    - facts                (deferred — needs Atlas Vector Search)
    - category_summaries   (deferred)

If `uri` is empty / unset, `MongoDB.connect()` returns `None`. The rest
of the agent treats that as "no persistent memory" — hot cache still
works, but core / transcript / recall become no-ops. This keeps local
dev viable without an Atlas cluster.
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
        self.core_memory: Collection = self.db["core_memory"]
        self.messages: Collection = self.db["messages"]
        self.reminders: Collection = self.db["reminders"]
        self.conversation_state: Collection = self.db["conversation_state"]

    # --- Public API -----------------------------------------------------

    @classmethod
    def connect(cls, *, uri: str | None, db_name: str) -> Optional["MongoDB"]:
        """Connect to Mongo if a URI is provided; otherwise return None.

        URI resolution: explicit arg → `MONGO_URI` env → None.
        """
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
        """Idempotent index assertions for the collections we own.

        Atlas Search / Vector indexes are managed separately (see
        `agent/atlas_indexes.json` per plan); only btree indexes here.
        """
        self.core_memory.create_index([("user_id", ASCENDING)], unique=True)

        self.messages.create_index(
            [("user_id", ASCENDING), ("created_at", DESCENDING)],
        )
        self.messages.create_index([("turn_id", ASCENDING)])

        self.reminders.create_index(
            [("user_id", ASCENDING), ("fire_at", ASCENDING)],
        )

    def close(self) -> None:
        self.client.close()
