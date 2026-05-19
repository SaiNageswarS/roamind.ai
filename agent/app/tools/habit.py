"""Habit read tool — exposes the user's tracked habits to the agent.

Read-only; the gateway owns writes via `/habit_*` slash commands.
"""

from __future__ import annotations

from datetime import datetime, timedelta, timezone

from langchain_core.tools import tool

from ._context import get_current_user, get_long_term


@tool
def get_habit_summary(period: str = "today", habit_name: str = "") -> str:
    """Rendered view of the user's tracked habits with counters for a period.

    This is the canonical, single-call view of habit metadata
    (slug, name, polarity, description) **plus** the user's actual
    behaviour (positive / negative counts) for `period`. It should
    ground most personalized advice.

    **Call this proactively — do not wait for the user to ask.**

    **Use for:**
      - **Health / wellness advice:** before suggesting diet, exercise,
        sleep, or lifestyle changes, check what the user is already
        doing (or skipping). If the user shares a health report, lab
        result, or symptom, pair it with their habit performance —
        e.g. low workouts + high cholesterol → reinforce exercise;
        high junk-food count + GI issue → flag the correlation.
      - **Day / week planning:** at the start of a planning request
        ("plan my day", "what should I focus on?"), look up today /
        this-week counters first so the plan reinforces lagging
        positive habits and counters frequent negative ones.
      - **Reflective queries:** "how often did I X this week?", streak
        check-ins, weekly review prompts.
      - **Progress reports:** when the user asks how they're doing.
      - **Disambiguation:** resolving a habit the user mentions by an
        approximate name to its canonical slug.

    The polarity tag tells you the user's intent for that habit:
    `positive` means more is better, `negative` means fewer is better,
    `both` is neutral.

    Habits cannot be created or mutated from this tool — that happens
    via gateway slash commands (`/habit_add`, `/habit_inc`,
    `/habit_dec`, `/habit_desc`).

    Args:
        period: "today" | "week" | "month". Defaults to "today".
        habit_name: Optional habit slug or substring to filter to one
            habit. Empty returns all tracked habits.
    """
    user_id = get_current_user()
    long_term = get_long_term()
    if not user_id or long_term is None:
        return "get_habit_summary: unavailable"

    habits = long_term.habits.list_habits(user_id)
    if not habits:
        return (
            "No habits tracked yet. The user can run `/habit_add <name> "
            "[positive|negative|both] [-- description]` to start tracking."
        )

    period = (period or "today").strip().lower()
    tz_name = "Asia/Kolkata"
    profile = long_term.profile.get(user_id)
    if profile and getattr(profile, "timezone", None):
        tz_name = profile.timezone or tz_name

    try:
        import zoneinfo
        tz = zoneinfo.ZoneInfo(tz_name)
    except Exception:
        tz = timezone.utc

    now_local = datetime.now(tz)

    if period == "today":
        date_str = now_local.strftime("%Y-%m-%d")
        totals = long_term.habits.current_day_totals(user_id, date_str)
        label = f"today ({date_str}, {tz_name})"
    elif period == "week":
        wd = now_local.isoweekday()  # Mon=1..Sun=7
        start = (now_local - timedelta(days=wd - 1)).strftime("%Y-%m-%d")
        end = now_local.strftime("%Y-%m-%d")
        totals = long_term.habits.current_week_totals(
            user_id, from_date=start, to_date=end
        )
        label = f"this week ({start}..{end}, {tz_name})"
    elif period == "month":
        start = now_local.replace(day=1).strftime("%Y-%m-%d")
        end = now_local.strftime("%Y-%m-%d")
        totals = long_term.habits.current_week_totals(
            user_id, from_date=start, to_date=end
        )
        label = f"this month ({start}..{end}, {tz_name})"
    else:
        return (
            f"get_habit_summary: unsupported period {period!r} "
            "(use today|week|month)"
        )

    needle = (habit_name or "").strip().lower()
    if needle:
        habits = [h for h in habits if needle in h.slug.lower() or needle in h.name.lower()]
        if not habits:
            return f"{label}: no habit matching {habit_name!r}"

    lines = [f"Habit summary for {label}:"]
    for h in sorted(habits, key=lambda x: x.slug):
        v = totals.get(h.slug, {"positive": 0, "negative": 0})
        head = f"- {h.slug} ({h.name}) polarity={h.polarity} | +{v['positive']} / -{v['negative']}"
        if h.description:
            head += f"\n    {h.description}"
        lines.append(head)
    return "\n".join(lines)
