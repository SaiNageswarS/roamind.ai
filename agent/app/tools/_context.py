"""Per-turn context wiring for stateful tools.

Tools registered with LangChain receive only their declared args. The
`intake` graph node stashes the active `user_id` here so tools like
`get_habit_summary` can resolve it without changing their LangChain
signatures. `bind_long_term_memory` injects the `LongTermMemory`
composite once at graph construction.
"""

from __future__ import annotations

from contextvars import ContextVar
from typing import Optional, TYPE_CHECKING

if TYPE_CHECKING:  # pragma: no cover
    from ..memory.long_term import LongTermMemory

_current_user_id: ContextVar[str] = ContextVar("roamind_current_user_id", default="")
_long_term_ref: Optional["LongTermMemory"] = None


def bind_long_term_memory(long_term: "LongTermMemory") -> None:
    """Wire the LongTermMemory instance that habit tools read from."""
    global _long_term_ref
    _long_term_ref = long_term


def set_current_user(user_id: str) -> None:
    """Set the user_id for the current turn. Call from `intake`."""
    _current_user_id.set(user_id or "")


def get_current_user() -> str:
    return _current_user_id.get()


def get_long_term() -> Optional["LongTermMemory"]:
    return _long_term_ref
