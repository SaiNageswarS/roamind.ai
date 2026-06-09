"""Shared HTTP helpers for tool integrations.

All functions return either the parsed JSON value or a short error
string — never raise — so tools can produce LLM-readable failure
messages instead of aborting the agent turn.
"""

from __future__ import annotations

import json
from typing import Any

import httpx
import structlog

log = structlog.get_logger("roamind.agent.tools.http")

HTTP_TIMEOUT = 10.0
USER_AGENT = "roamind-agent/0.1 (+https://github.com/roamind)"


def http_get_json(
    url: str,
    *,
    params: dict[str, str] | None = None,
    headers: dict[str, str] | None = None,
) -> Any:
    """GET `url` and parse JSON. Return parsed value, or an error string."""
    hdrs = {"User-Agent": USER_AGENT}
    if headers:
        hdrs.update(headers)
    try:
        with httpx.Client(timeout=HTTP_TIMEOUT, follow_redirects=True) as client:
            resp = client.get(url, params=params, headers=hdrs)
    except httpx.HTTPError as e:
        log.warning("tool http error", url=url, err=str(e))
        return f"network error: {e}"
    if resp.status_code >= 400:
        return f"upstream {resp.status_code}: {resp.text[:160]}"
    try:
        return resp.json()
    except json.JSONDecodeError as e:
        return f"bad json: {e}"


def http_post_json(
    url: str,
    *,
    json_body: dict[str, Any],
    headers: dict[str, str] | None = None,
) -> Any:
    """POST JSON to `url` and parse the JSON response, or return an error string."""
    hdrs = {"User-Agent": USER_AGENT, "Content-Type": "application/json"}
    if headers:
        hdrs.update(headers)
    try:
        with httpx.Client(timeout=HTTP_TIMEOUT, follow_redirects=True) as client:
            resp = client.post(url, json=json_body, headers=hdrs)
    except httpx.HTTPError as e:
        log.warning("tool http error", url=url, err=str(e))
        return f"network error: {e}"
    if resp.status_code >= 400:
        return f"upstream {resp.status_code}: {resp.text[:160]}"
    try:
        return resp.json()
    except json.JSONDecodeError as e:
        return f"bad json: {e}"
