"""Per-turn context wiring for stateful tools.

Tools registered with LangChain receive only their declared args. The
`intake` graph node stashes the active `user_id` here so tools like
`get_habit_summary` can resolve it without changing their LangChain
signatures. `bind_long_term_memory` injects the `LongTermMemory`
composite once at graph construction.
"""

from __future__ import annotations

from typing import Optional, TYPE_CHECKING

if TYPE_CHECKING:  # pragma: no cover
    from ..memory.long_term import LongTermMemory

# Plain module-level globals: the agent loop processes one task at a
# time, so per-turn state is safe to stash here. A ContextVar would be
# wrong — LangGraph runs each node in its own asyncio Task with a
# copied context, so a value set inside `intake` would not be visible
# to the `act` node where tools execute.
_current_user_id: str = ""
_long_term_ref: Optional["LongTermMemory"] = None


def bind_long_term_memory(long_term: "LongTermMemory") -> None:
    """Wire the LongTermMemory instance that habit tools read from."""
    global _long_term_ref
    _long_term_ref = long_term


def set_current_user(user_id: str) -> None:
    """Set the user_id for the current turn. Call from `intake`."""
    global _current_user_id
    _current_user_id = user_id or ""


def get_current_user() -> str:
    return _current_user_id


def get_long_term() -> Optional["LongTermMemory"]:
    return _long_term_ref


def get_long_term_memory() -> "LongTermMemory":
    """Get the LongTermMemory instance with the current user context set."""
    ltm = get_long_term()
    if ltm is None:
        raise RuntimeError("LongTermMemory not initialized")
    # Attach user_id as an attribute for convenience
    ltm.user_id = get_current_user()
    return ltm
