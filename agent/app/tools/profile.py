"""Profile management tools for user configuration (mode, preferences, etc.)."""

from __future__ import annotations

from langchain_core.tools import tool

from ._context import get_long_term_memory
from ..memory import UserProfile


VALID_MODES = {"maintenance", "focus", "vacation", "recovery", "deep_work"}


@tool
def set_user_mode(mode: str) -> str:
    """Set the user's scheduling mode for habit prioritization.
    
    Valid modes:
    - maintenance: Normal mode, balance all habits equally
    - focus: Prioritize work and productivity habits
    - vacation: Prioritize relaxation and recovery
    - recovery: Prioritize health and healing activities
    - deep_work: Minimize interruptions, consolidated feedback
    
    Args:
        mode: The scheduling mode to set (must be one of the valid modes above).
    
    Returns:
        Confirmation message with the new mode and its guidance.
    """
    mode = mode.lower().strip()
    if mode not in VALID_MODES:
        valid_str = ", ".join(sorted(VALID_MODES))
        return f"Invalid mode '{mode}'. Valid modes are: {valid_str}"
    
    ltm = get_long_term_memory()
    user_id = ltm.user_id
    
    # Upsert the mode field
    profile = ltm.profile.upsert(user_id, {"mode": mode})
    
    if profile is None:
        return f"Failed to update mode to '{mode}'."
    
    guidance = profile.mode_instructions()
    return f"Mode changed to '{mode}'.\n\n{guidance}"


@tool
def get_user_mode() -> str:
    """Get the user's current scheduling mode.
    
    Returns:
        The current mode name and its guidance instructions.
    """
    ltm = get_long_term_memory()
    user_id = ltm.user_id
    profile = ltm.profile.get(user_id)
    
    if profile is None:
        return "No profile found. Default mode is 'maintenance'."
    
    mode = profile.mode
    guidance = profile.mode_instructions()
    return f"Current mode: {mode}\n\n{guidance}"


__all__ = [
    "set_user_mode",
    "get_user_mode",
    "VALID_MODES",
]
