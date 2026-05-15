"""LangGraph definition for the Roamind agent.

v1 graph (matches plan.md Phase 3, step 5):

    intake -> plan -> act -> compact_err -> plan (on error)
                    \\-> respond

For now this is a minimal skeleton: `intake` loads context, `plan` decides
either tool-call or direct response, `respond` formats the final reply.
Tooling (`act`, `compact_err`) is wired as pass-through stubs so the
shape is in place for Phase 3 iteration.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass, field
from typing import Any, Dict, Optional

from langgraph.graph import END, StateGraph

from .envelope import TaskIn, TaskOut


@dataclass
class AgentState:
    task_in: TaskIn
    # Memory & planning state — placeholders for Phase 3 expansion.
    core_memory: Dict[str, Any] = field(default_factory=dict)
    short_term: list = field(default_factory=list)
    recalled_facts: list = field(default_factory=list)
    plan: Optional[str] = None
    tool_result: Optional[Any] = None
    last_error: Optional[str] = None
    # Final output.
    task_out: Optional[TaskOut] = None


# --- Nodes ---------------------------------------------------------------

def intake(state: AgentState) -> AgentState:
    """Load core memory, recent turns, relevant facts.

    Stubbed for v1: real implementation pulls from Mongo (`core_memory`,
    `conversations`, `facts` with $vectorSearch).
    """
    return state


def plan(state: AgentState) -> AgentState:
    """LLM-driven planning step.

    Stubbed: choose `respond` directly. Replace with LLMClient call that
    decides tool invocation vs direct reply.
    """
    state.plan = "respond"
    return state


def act(state: AgentState) -> AgentState:
    """ToolNode wrapper (MCP + in-process tools). Stubbed."""
    return state


def compact_err(state: AgentState) -> AgentState:
    """Normalize tool exceptions into LLM-friendly summaries. Stubbed."""
    state.last_error = None
    return state


def respond(state: AgentState) -> AgentState:
    """Compose the user-facing TaskOut and emit it via the graph result.

    v1 behaviour: echo the user's text. The real agent persists the turn
    and uses an LLM to format the reply.
    """
    incoming = state.task_in
    state.task_out = TaskOut(
        id=str(uuid.uuid4()),
        trace_id=incoming.trace_id,
        in_reply_to=incoming.id,
        user_id=incoming.user_id,
        channel=incoming.channel,
        chat_ref=incoming.channel_msg_id,
        intent="reply",
        payload=f"echo: {incoming.text}",
    )
    return state


# --- Routing -------------------------------------------------------------

def _after_plan(state: AgentState) -> str:
    return "respond"  # Phase 3 will branch to "act" when a tool call is planned.


def _after_act(state: AgentState) -> str:
    return "compact_err" if state.last_error else "respond"


# --- Graph builder -------------------------------------------------------

def build_graph():
    g = StateGraph(AgentState)
    g.add_node("intake", intake)
    g.add_node("plan", plan)
    g.add_node("act", act)
    g.add_node("compact_err", compact_err)
    g.add_node("respond", respond)

    g.set_entry_point("intake")
    g.add_edge("intake", "plan")
    g.add_conditional_edges("plan", _after_plan, {"act": "act", "respond": "respond"})
    g.add_conditional_edges("act", _after_act, {"compact_err": "compact_err", "respond": "respond"})
    g.add_edge("compact_err", "plan")
    g.add_edge("respond", END)

    return g.compile()
