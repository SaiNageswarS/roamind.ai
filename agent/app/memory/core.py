"""Core memory: small per-user document always injected into the prompt.

Per plan.md memory tier "Core". The data is intentionally a free-form
key/value bag (preferences, timezone, identity). Tools may update it
via `update_core_memory`; the graph reads it once per turn in `intake`.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import Any, Optional

import structlog

from ..db import MongoDB
from .models import CoreMemory

log = structlog.get_logger("roamind.agent.memory.core")


class CoreMemoryStore:
    """CRUD over the `core_memory` collection, keyed by `user_id`."""

    def __init__(self, mongo: Optional[MongoDB]):
        self._mongo = mongo

    # --- Public API -----------------------------------------------------

    def get(self, user_id: str) -> Optional[CoreMemory]:
        if self._mongo is None or not user_id:
            return None
        doc = self._mongo.core_memory.find_one({"user_id": user_id})
        if doc is None:
            return None
        doc.pop("_id", None)
        return CoreMemory.model_validate(doc)

    def upsert(self, user_id: str, data: dict[str, Any]) -> Optional[CoreMemory]:
        """Merge `data` into the user's core memory. Returns the new doc."""
        if self._mongo is None or not user_id:
            return None
        now = datetime.now(timezone.utc)
        existing = self.get(user_id)
        merged = {**(existing.data if existing else {}), **data}
        self._mongo.core_memory.update_one(
            {"user_id": user_id},
            {"$set": {"user_id": user_id, "data": merged, "updated_at": now}},
            upsert=True,
        )
        return CoreMemory(user_id=user_id, data=merged, updated_at=now)

    def render_for_prompt(self, mem: Optional[CoreMemory]) -> str:
        """Compact one-line-per-key rendering for the system prompt.

        Kept here (not in graph.py) so the rendering format lives next to
        the data shape it depends on.
        """
        if mem is None or not mem.data:
            return ""
        lines = [f"- {k}: {v}" for k, v in mem.data.items()]
        return "Known about the user:\n" + "\n".join(lines)
