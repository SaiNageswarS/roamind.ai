"""Todoist integration — fetch + render the user's tasks.

Public surface:

  - `TodoistTool` — class with two methods:
      * `fetch(...)` returns structured `{"active": [...], "completed": [...]}`.
      * `render(data)` formats the structured payload as Markdown.
    The two halves are independent so callers can re-render cached
    data, snapshot fixtures for tests, or pipe `fetch()` output
    elsewhere without re-hitting the API.

  - `get_todoist_tasks` — thin `@tool` adapter for LangChain that
    composes `fetch` + `render` and returns the string.

The module is runnable standalone — `.env` is loaded automatically
(searched upwards from the current directory):

    # from agent/
    python -m app.tools.todoist [--no-completed] [--days 14] [--json]

    # from the repo root
    cd agent && python -m app.tools.todoist
    # or, without changing directory:
    PYTHONPATH=agent python -m app.tools.todoist

Set `TODOIST_API_TOKEN` in your `.env` (Settings → Integrations →
Developer in Todoist) or export it in the shell.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timedelta, timezone
from typing import Any

from dotenv import load_dotenv
from langchain_core.tools import tool

from ._http import http_get_json


_ACTIVE_TASKS_URL = "https://api.todoist.com/api/v1/tasks"
_COMPLETED_URL = "https://api.todoist.com/api/v1/tasks/completed/by_completion_date"
_MAX_ACTIVE = 100
_MAX_COMPLETED = 50
_PAGE_LIMIT = 200  # Todoist v1 paginated endpoints cap.
_PAGE_FETCH_SAFETY_CAP = 10  # Hard cap on pagination loops.
_DEFAULT_COMPLETED_DAYS = 30

_PRIORITY_LABEL = {4: "p1", 3: "p2", 2: "p3", 1: "p4"}


class TodoistTool:
    """Todoist REST/Sync client with separate fetch + render concerns.

    Stateless aside from the API token. Safe to instantiate per call or
    reuse — there's no connection pool to manage.
    """

    def __init__(self, api_token: str | None = None) -> None:
        self.api_token = (api_token or os.getenv("TODOIST_API_TOKEN", "")).strip() or None

    # --- Public API -----------------------------------------------------

    def is_configured(self) -> bool:
        return bool(self.api_token)

    def fetch(
        self,
        *,
        include_completed: bool = True,
        completed_days: int = _DEFAULT_COMPLETED_DAYS,
    ) -> dict[str, Any]:
        """Pull active and (optionally) recently completed tasks.

        Returns a dict shaped like:

            {
                "active":    list[dict] | None,
                "completed": list[dict] | None,   # only when include_completed
                "completed_days": int,
                "error":     str | None,          # set on failure
            }

        Errors from the API are reported in `error` instead of raised so
        callers (including the LangChain @tool adapter) can surface a
        short string without crashing the agent turn.
        """
        if not self.is_configured():
            return {"error": "not configured (TODOIST_API_TOKEN missing)"}

        completed_days = max(1, min(int(completed_days or _DEFAULT_COMPLETED_DAYS), 30))

        active = self._fetch_active()
        if isinstance(active, str):
            return {"error": active}

        out: dict[str, Any] = {
            "active": active,
            "completed_days": completed_days,
            "error": None,
        }

        if include_completed:
            completed = self._fetch_completed(completed_days)
            if isinstance(completed, str):
                # Treat completed-fetch failure as soft: still return active.
                out["completed"] = None
                out["completed_error"] = completed
            else:
                out["completed"] = completed[:_MAX_COMPLETED]

        return out

    def render(self, data: dict[str, Any]) -> str:
        """Format `fetch()` output as Markdown for the LLM (or terminal).

        Active and recently-completed tasks are interleaved in a single
        parent → sub-task hierarchy so the LLM can see progress in
        context (a parent task and its done/pending children together).
        """
        if data.get("error"):
            return f"get_todoist_tasks: {data['error']}"

        active = data.get("active") or []
        completed = data.get("completed") or []
        days = data.get("completed_days", _DEFAULT_COMPLETED_DAYS)

        lines: list[str] = []
        header = (
            f"## Todoist tasks — {len(active)} active, "
            f"{len(completed)} completed in last {days}d"
        )
        lines.append(header)

        if active or completed:
            lines.extend(_render_hierarchy(active, completed))
        else:
            lines.append("\n_None._")

        if data.get("completed_error"):
            lines.append(f"\n_completed-fetch error: {data['completed_error']}_")

        return "\n".join(lines)

    # --- Private --------------------------------------------------------

    def _headers(self) -> dict[str, str]:
        return {
            "Authorization": f"Bearer {self.api_token or ''}",
            "Accept": "application/json",
        }

    def _fetch_active(self) -> list[dict[str, Any]] | str:
        return self._paginate(_ACTIVE_TASKS_URL, params={}, items_key="results")

    def _fetch_completed(self, days: int) -> list[dict[str, Any]] | str:
        now = datetime.now(timezone.utc)
        since = (now - timedelta(days=days)).strftime("%Y-%m-%dT%H:%M:%SZ")
        until = now.strftime("%Y-%m-%dT%H:%M:%SZ")
        return self._paginate(
            _COMPLETED_URL,
            params={"since": since, "until": until},
            items_key="items",
            max_items=_MAX_COMPLETED,
        )

    def _paginate(
        self,
        url: str,
        *,
        params: dict[str, str],
        items_key: str,
        max_items: int | None = None,
    ) -> list[dict[str, Any]] | str:
        """Walk a v1 cursor-paginated endpoint and return collected items.

        Stops at `max_items` or `_PAGE_FETCH_SAFETY_CAP` pages, whichever
        comes first. Returns a short error string on transport failure.
        """
        items: list[dict[str, Any]] = []
        cursor: str | None = None
        for _ in range(_PAGE_FETCH_SAFETY_CAP):
            page_params = dict(params)
            page_params["limit"] = str(_PAGE_LIMIT)
            if cursor:
                page_params["cursor"] = cursor
            data = http_get_json(url, params=page_params, headers=self._headers())
            if isinstance(data, str):
                return data
            if not isinstance(data, dict):
                return "bad response"
            page = data.get(items_key) or []
            items.extend(p for p in page if isinstance(p, dict))
            if max_items is not None and len(items) >= max_items:
                return items[:max_items]
            cursor = data.get("next_cursor")
            if not cursor:
                break
        return items


# --- LangChain adapter --------------------------------------------------


@tool
def get_todoist_tasks(
    include_completed: bool = True,
    completed_days: int = 30,
) -> str:
    """Read the user's Todoist tasks — active and recently completed.

    Todoist is the user's external task / to-do system covering ongoing
    work, personal errands, appointments, and event commitments. Pair
    this with `get_habit_summary` whenever you plan the user's day or
    week, advise on workload, or check what they're juggling — habits
    cover recurring behaviours, Todoist covers discrete tasks and
    deadlines.

    **Call this proactively** for:
      - "plan my day / week", "what should I focus on?", standup prompts
      - workload, capacity, or prioritisation questions
      - reminders / deadlines ("anything due?", "what's overdue?")
      - reflective check-ins ("what did I get done this week?")
      - any request where missing the user's task list would lead to
        advice that ignores real commitments

    Active tasks are rendered as **Markdown**: each top-level (parent)
    task is an H1 heading with its description as a paragraph beneath
    it, and sub-tasks are nested bullet lists (descriptions hang as
    indented continuation lines under their bullet). Each task carries
    its priority (p1 = highest .. p4 = lowest), due date/time, and
    labels. Completed tasks are listed flat as a recent tail for
    context on what's been getting done.

    Args:
        include_completed: Include recently completed tasks for context.
            Defaults to True.
        completed_days: Lookback window in days for completed tasks
            (1-30). Defaults to 30.
    """
    client = TodoistTool()
    data = client.fetch(
        include_completed=include_completed,
        completed_days=completed_days,
    )
    return client.render(data)


# --- Rendering helpers (module-private) ---------------------------------


def _task_sort_key(t: dict[str, Any]) -> tuple[int, int, str, int, int]:
    # Active tasks first, then completed; within each group: due-date, priority, order.
    completed_rank = 1 if t.get("_completed") else 0
    due = t.get("due") or {}
    due_date = (due.get("date") or "") if isinstance(due, dict) else ""
    has_due = 0 if due_date else 1
    prio = -int(t.get("priority") or 1)  # Todoist priority: 4 = highest.
    order = int(t.get("order") or t.get("child_order") or 0)
    return (completed_rank, has_due, due_date, prio, order)


def _render_hierarchy(
    active: list[dict[str, Any]],
    completed: list[dict[str, Any]],
) -> list[str]:
    """Merge active + completed tasks into a single parent → child tree.

    Completed tasks are tagged so the renderer can mark them with ✓ and
    their completion date. Active siblings render before completed ones
    under the same parent.
    """
    tasks: list[dict[str, Any]] = []
    for t in active:
        if isinstance(t, dict) and t.get("id") is not None:
            tasks.append({**t, "_completed": False})
    for c in completed:
        if isinstance(c, dict) and c.get("id") is not None:
            tasks.append({**c, "_completed": True})

    by_id: dict[str, dict[str, Any]] = {str(t["id"]): t for t in tasks}
    children: dict[str, list[dict[str, Any]]] = {}
    roots: list[dict[str, Any]] = []
    for t in tasks:
        pid = t.get("parent_id")
        pid_str = str(pid) if pid not in (None, "") else ""
        if pid_str and pid_str in by_id:
            children.setdefault(pid_str, []).append(t)
        else:
            roots.append(t)

    for siblings in children.values():
        siblings.sort(key=_task_sort_key)
    roots.sort(key=_task_sort_key)

    out: list[str] = []
    budget = [_MAX_ACTIVE + _MAX_COMPLETED]

    def walk(node: dict[str, Any], depth: int) -> None:
        if budget[0] <= 0:
            return
        out.extend(_format_task(node, depth))
        budget[0] -= 1
        for child in children.get(str(node["id"]), []):
            walk(child, depth + 1)

    for root in roots:
        walk(root, 0)
        if budget[0] <= 0:
            break

    if budget[0] <= 0 and len(by_id) > (_MAX_ACTIVE + _MAX_COMPLETED):
        omitted = len(by_id) - (_MAX_ACTIVE + _MAX_COMPLETED)
        out.append(f"… ({omitted} more tasks omitted)")
    return out


def _format_task(t: dict[str, Any], depth: int) -> list[str]:
    """Markdown for a single task. Depth 0 → H1 heading, deeper → nested bullets.

    Completed tasks are prefixed with `✓ ` and suffixed with `(completed YYYY-MM-DD)`.
    """
    content = (t.get("content") or "").strip() or "(untitled)"
    description = (t.get("description") or "").strip()
    priority = _PRIORITY_LABEL.get(int(t.get("priority") or 1), "p4")

    due = t.get("due") or {}
    due_str = ""
    if isinstance(due, dict):
        due_str = due.get("datetime") or due.get("date") or ""

    labels = t.get("labels") or []
    label_str = " ".join(f"@{l}" for l in labels if isinstance(l, str))

    meta_bits = [f"[{priority}]"]
    if due_str:
        meta_bits.append(f"due={due_str}")
    if label_str:
        meta_bits.append(label_str)
    meta = " ".join(meta_bits)

    done_prefix = ""
    done_suffix = ""
    if t.get("_completed"):
        done_prefix = "✓ "

    desc = ""
    if description:
        desc = description.replace("\r", "").strip()
        if len(desc) > 400:
            desc = desc[:400] + "…"

    if depth == 0:
        lines = ["", f"# {done_prefix}{meta} {content}{done_suffix}".rstrip()]
        if desc:
            lines.append(desc.replace("\n", " "))
        return lines

    indent = "  " * (depth - 1)
    lines = [f"{indent}- {done_prefix}{meta} {content}{done_suffix}".rstrip()]
    if desc:
        cont_indent = indent + "  "
        for line in desc.split("\n"):
            line = line.strip()
            if line:
                lines.append(f"{cont_indent}{line}")
    return lines


# --- Script entrypoint --------------------------------------------------


def _main(argv: list[str] | None = None) -> int:
    # Load .env from the nearest parent dir so the script picks up
    # TODOIST_API_TOKEN whether invoked from agent/ or the repo root.
    load_dotenv()

    parser = argparse.ArgumentParser(
        prog="python -m app.tools.todoist",
        description="Fetch and render the current Todoist tasks for the configured user.",
    )
    parser.add_argument(
        "--no-completed",
        dest="include_completed",
        action="store_false",
        help="Skip the recently-completed task tail.",
    )
    parser.add_argument(
        "--days",
        type=int,
        default=_DEFAULT_COMPLETED_DAYS,
        help=f"Lookback window in days for completed tasks (1-30, default {_DEFAULT_COMPLETED_DAYS}).",
    )
    parser.add_argument(
        "--json",
        dest="as_json",
        action="store_true",
        help="Emit the raw fetch() payload as JSON instead of rendered Markdown.",
    )
    args = parser.parse_args(argv)

    client = TodoistTool()
    if not client.is_configured():
        print("TODOIST_API_TOKEN is not set.", file=sys.stderr)
        return 2

    data = client.fetch(
        include_completed=args.include_completed,
        completed_days=args.days,
    )
    if args.as_json:
        print(json.dumps(data, indent=2, default=str))
    else:
        print(client.render(data))
    return 0 if not data.get("error") else 1


if __name__ == "__main__":
    raise SystemExit(_main())
