"""Roamind agent worker loop.

Two modes:

  redis (default)
    Consumes `tasks.in` via XREADGROUP, runs each envelope through the
    LangGraph, and emits the resulting `TaskOut` to `tasks.out`. Failed
    messages are retried up to `MAX_DELIVERIES` then routed to `tasks.dlq`.
    This worker is designed to process one message at a time per process
    instance. For concurrency, run multiple instances behind a process
    manager.

  stdin
    Reads user input from stdin interactively, runs it through the same
    LangGraph, and prints the reply to stdout. Redis is still required for
    short-term memory (chat:hot:{user_id}) and the LLM response cache, but
    the task-transport streams (tasks.in / tasks.out) are not used.
    Use this mode for local development: no gateway, no gRPC, no roamind-cli.

The graph is built once at startup and shared across all requests in both modes.
"""

from __future__ import annotations

import argparse
import configparser
import logging
import os
import signal
import sys
import uuid
from datetime import datetime, timezone
from pathlib import Path

import redis
import structlog
from dotenv import load_dotenv

from .db import MongoDB
from .envelope import TaskIn, parse_task_in
from .graph import AgentState, RoamindGraph
from .llm import LLMClient
from .memory import LongTermMemory, ShortTermMemory
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
CONFIG_PATH = Path(__file__).resolve().parent.parent.parent / "config.ini"

log = structlog.get_logger("roamind.agent")

_should_stop = False


def main() -> int:
    load_dotenv()
    _configure_logging()
    _install_signal_handlers()

    args = _parse_args()

    mongo: MongoDB | None = None
    try:
        cfg = configparser.ConfigParser()
        cfg.read(CONFIG_PATH)
        client = new_redis_client()

        if args.mode == "redis":
            ensure_group(client, STREAM_TASKS_IN, GROUP_AGENT)

        llm = LLMClient.from_config(cfg, redis_client=client)
        mongo = MongoDB.connect(
            uri=cfg.get("mongo", "uri", fallback=""),
            db_name=cfg.get("mongo", "db_name", fallback="roamind"),
        )
        short_term = ShortTermMemory(
            client,
            max_messages=cfg.getint("memory", "short_term_max_messages", fallback=8),
        )
        long_term = LongTermMemory(mongo)
    except Exception as e:
        log.error("startup failed", err=str(e))
        return 1

    graph = RoamindGraph.build(llm, short_term, long_term)

    try:
        if args.mode == "redis":
            _run_redis_worker(client, graph)
        else:
            _stdin_loop(graph)
    finally:
        client.close()
        if mongo is not None:
            mongo.close()
    return 0


def _run_redis_worker(
    client: redis.Redis,
    graph,
) -> None:
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


def _stdin_loop(graph) -> None:
    """Interactive REPL over stdin/stdout. Redis is used for memory and LLM
    cache, but task-transport streams are bypassed entirely."""
    user_id = os.getenv("CLI_USER_ID", "local")
    channel = "cli"
    log.info("agent started in stdin mode", user_id=user_id)

    print("Roamind local agent. Type your message and press Enter. Ctrl+D or Ctrl+C to exit.\n")

    while not _should_stop:
        try:
            text = input("> ").strip()
        except EOFError:
            print()
            break
        except KeyboardInterrupt:
            print()
            break

        if not text:
            continue

        task_in = TaskIn(
            id=str(uuid.uuid4()),
            trace_id=str(uuid.uuid4()),
            user_id=user_id,
            channel=channel,
            text=text,
            received_at=datetime.now(timezone.utc),
        )

        try:
            final = graph.invoke(AgentState(task_in=task_in))
        except Exception as e:
            log.exception("graph invoke failed", err=str(e))
            print(f"[error] {e}\n")
            continue

        task_out = final["task_out"] if isinstance(final, dict) else getattr(final, "task_out", None)
        if task_out is None:
            print("[error] agent produced no response\n")
            continue

        print(f"\n{task_out.payload}\n")

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


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Roamind agent")
    parser.add_argument(
        "--mode",
        choices=["redis", "stdin"],
        default="redis",
        help="Transport mode: 'redis' (default) consumes tasks.in stream; "
             "'stdin' reads from stdin for local interactive use.",
    )
    return parser.parse_args()


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


if __name__ == "__main__":
    raise SystemExit(main())
