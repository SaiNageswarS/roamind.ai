"""Profile management tools for user configuration (mode, preferences, etc.)."""

from __future__ import annotations

import structlog
from langchain_core.tools import tool

from ._context import get_long_term_memory
from ..memory import UserProfile

log = structlog.get_logger("roamind.agent.tools.profile")


@tool
def get_user_mode() -> str:
    """Get the user's current scheduling mode.
    
    Returns:
        The current mode name and its guidance instructions.
    """
    log.info("get_user_mode")
    ltm = get_long_term_memory()
    user_id = ltm.user_id
    profile = ltm.profile.get(user_id)
    
    if profile is None:
        return "No profile found. Default mode is 'maintenance'."
    
    mode = profile.mode
    guidance = profile.mode_instructions()
    return f"Current mode: {mode}\n\n{guidance}"


__all__ = [
    "get_user_mode",
]
