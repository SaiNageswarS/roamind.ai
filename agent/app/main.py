"""Roamind agent worker loop.

Consumes `tasks.in` via XREADGROUP, runs each envelope through the
LangGraph, and emits the resulting `TaskOut` to `tasks.out`. Failed
messages are retried up to `MAX_DELIVERIES` then routed to `tasks.dlq`.
"""

from __future__ import annotations

import configparser
import logging
import os
import signal
import sys
from pathlib import Path

import redis
import structlog
from dotenv import load_dotenv
from langchain_anthropic import ChatAnthropic
from langchain_core.language_models import BaseChatModel

from .envelope import parse_task_in
from .graph import AgentState, RoamindGraph
from .stream import (
    GROUP_AGENT,
    STREAM_TASKS_IN,
    ensure_group,
    new_redis_client,
    pending_retry_count,
    xack_tasks_in,
    xadd_dlq,
    xadd_tasks_out,
    xreadgroup_tasks_in,
)

MAX_DELIVERIES = 5
CONFIG_PATH = Path(__file__).resolve().parent.parent / "config.ini"

log = structlog.get_logger("roamind.agent")

_should_stop = False


def main() -> int:
    load_dotenv()
    _configure_logging()
    _install_signal_handlers()

    try:
        cfg = configparser.ConfigParser()
        cfg.read(CONFIG_PATH)
        client = new_redis_client()
        ensure_group(client, STREAM_TASKS_IN, GROUP_AGENT)
        llm = _build_llm(cfg)
    except Exception as e:
        log.error("startup failed", err=str(e))
        return 1

    try:
        _run_loop(client, llm)
    finally:
        client.close()
    return 0


def _run_loop(client: redis.Redis, llm: BaseChatModel) -> None:
    graph = RoamindGraph.build(llm)
    log.info("agent started", stream=STREAM_TASKS_IN, group=GROUP_AGENT)

    while not _should_stop:
        try:
            streams = xreadgroup_tasks_in(client)
        except redis.exceptions.ConnectionError as e:
            log.error("redis connection error", err=str(e))
            continue

        for _stream_name, messages in streams:
            for msg_id, fields in messages:
                if _should_stop:
                    return
                _process_message(client, graph, msg_id, fields)

    log.info("agent stopping")


def _process_message(client: redis.Redis, graph, msg_id: str, fields: dict) -> None:
    raw = fields.get("payload")
    if not raw:
        log.warning("payload missing", msg_id=msg_id)
        xack_tasks_in(client, [msg_id])
        return

    try:
        task_in = parse_task_in(raw)
    except Exception as e:
        log.error("parse TaskIn failed", err=str(e), msg_id=msg_id)
        _ack_or_dlq(client, msg_id, raw)
        return

    log.info(
        "processing task",
        task_id=task_in.id,
        channel=task_in.channel,
        user_id=task_in.user_id,
    )

    try:
        final = graph.invoke(AgentState(task_in=task_in))
    except Exception as e:
        log.exception("graph invoke failed", err=str(e), task_id=task_in.id)
        _ack_or_dlq(client, msg_id, raw)
        return

    # LangGraph returns a dict-like state for dataclass schemas.
    task_out = final["task_out"] if isinstance(final, dict) else getattr(final, "task_out", None)
    if task_out is None:
        log.error("graph produced no TaskOut", task_id=task_in.id)
        _ack_or_dlq(client, msg_id, raw)
        return

    try:
        xadd_tasks_out(client, task_out.to_wire_json())
    except Exception as e:
        log.error("emit tasks.out failed", err=str(e), task_id=task_in.id)
        _ack_or_dlq(client, msg_id, raw)
        return

    xack_tasks_in(client, [msg_id])
    log.info(
        "replied",
        task_id=task_in.id,
        in_reply_to=task_out.in_reply_to,
        intent=task_out.intent,
    )


def _ack_or_dlq(client: redis.Redis, msg_id: str, raw: str | None) -> None:
    try:
        retries = pending_retry_count(client, msg_id)
    except Exception:
        retries = MAX_DELIVERIES  # fail-safe: don't loop on parse errors

    if retries >= MAX_DELIVERIES:
        try:
            xadd_dlq(client, raw or "", msg_id)
        finally:
            xack_tasks_in(client, [msg_id])
        log.warning("message moved to DLQ", msg_id=msg_id, retries=retries)


def _install_signal_handlers() -> None:
    def _handler(signum, _frame):
        global _should_stop
        log.info("shutdown signal received", signal=signum)
        _should_stop = True

    signal.signal(signal.SIGINT, _handler)
    signal.signal(signal.SIGTERM, _handler)


def _configure_logging() -> None:
    level = os.getenv("LOG_LEVEL", "INFO").upper()
    logging.basicConfig(stream=sys.stdout, level=level, format="%(message)s")
    structlog.configure(
        processors=[
            structlog.processors.add_log_level,
            structlog.processors.TimeStamper(fmt="iso"),
            structlog.processors.JSONRenderer(),
        ],
    )


def _build_llm(cfg: configparser.ConfigParser) -> BaseChatModel:
    """Construct the chat model from config.ini."""
    provider = cfg.get("llm", "provider", fallback="anthropic").lower()
    model = cfg.get("llm", "model", fallback="claude-sonnet-4-6")

    if provider == "anthropic":
        return ChatAnthropic(model=model)
    raise ValueError(f"Unsupported llm.provider: {provider}")


if __name__ == "__main__":
    raise SystemExit(main())
