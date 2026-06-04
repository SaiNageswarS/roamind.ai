"""Agent-facing tools.

Each integration lives in its own module:

    search.py    — web / github / news / research / maps / finance
    habit.py     — get_habit_summary (reads LongTermMemory)
    todoist.py   — TodoistTool class + get_todoist_tasks @tool adapter

All `@tool`-decorated callables are aggregated into `ALL_TOOLS` and
bound to the LLM in `graph.py`. Stateful tools (habits) get their
per-turn user_id and the LongTermMemory composite via the helpers in
`_context.py`, set once at graph construction (`bind_long_term_memory`)
and per-turn (`set_current_user`).
"""

from __future__ import annotations

from ._context import bind_long_term_memory, set_current_user
from .habit import get_habit_summary
from .profile import get_user_mode, set_user_mode
from .search import (
    SEARCH_TOOLS,
    finance_search,
    github_search,
    maps_search,
    news_search,
    research_search,
    web_search,
)
from .todoist import TodoistTool, get_todoist_tasks

ALL_TOOLS = [
    *SEARCH_TOOLS,
    get_habit_summary,
    get_todoist_tasks,
    set_user_mode,
    get_user_mode,
]

__all__ = [
    "ALL_TOOLS",
    "bind_long_term_memory",
    "set_current_user",
    # Re-exports for ad-hoc imports / tests.
    "TodoistTool",
    "get_habit_summary",
    "get_todoist_tasks",
    "web_search",
    "github_search",
    "news_search",
    "research_search",
    "finance_search",
    "maps_search",
]
