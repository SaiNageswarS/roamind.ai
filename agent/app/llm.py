"""Provider-agnostic LLM client with optional Redis response cache.

Wraps a LangChain `BaseChatModel` (currently `ChatAnthropic`) and adds a
content-addressable cache keyed by a stable hash of the input messages
and the relevant generation parameters. When a Redis client is provided
and a cache hit occurs, the underlying model is not invoked.

The cache stores serialized `AIMessage` objects via
`langchain_core.load.dumps`/`loads`, preserving content blocks,
`tool_calls`, and `response_metadata`.
"""

from __future__ import annotations

import configparser
import hashlib
import json
import logging
from typing import Any, Sequence

import redis
import structlog
from langchain_anthropic import ChatAnthropic
from langchain_core.language_models import BaseChatModel
from langchain_core.load import dumps as lc_dumps
from langchain_core.load import loads as lc_loads
from langchain_core.messages import AIMessage, BaseMessage

DEFAULT_CACHE_TTL_SECONDS = 60 * 60 * 24  # 24h
CACHE_KEY_PREFIX = "llm:cache:v1:"

log = structlog.get_logger("roamind.agent.llm")


class LLMClient:
    """Thin wrapper over a chat model with Redis-backed response caching.

    Cache semantics:
        - Key = sha256 of (model, temperature, max_tokens, normalized
          messages, sorted bind kwargs).
        - Value = `langchain_core.load.dumps(AIMessage)`.
        - TTL = `cache_ttl_seconds`.
        - A `None` `redis_client` disables caching transparently.

    The wrapper is intentionally minimal: it exposes `invoke`,
    `bind_tools`, and the underlying `chat_model` for advanced use.
    """

    def __init__(
        self,
        chat_model: BaseChatModel,
        *,
        redis_client: redis.Redis | None = None,
        cache_ttl_seconds: int = DEFAULT_CACHE_TTL_SECONDS,
        cache_enabled: bool = True,
    ) -> None:
        self.chat_model = chat_model
        self._redis = redis_client
        self._ttl = cache_ttl_seconds
        self._cache_enabled = cache_enabled and redis_client is not None
        self._bound_kwargs: dict[str, Any] = {}

    # --- Public API -----------------------------------------------------

    @classmethod
    def from_config(
        cls,
        cfg: configparser.ConfigParser,
        *,
        redis_client: redis.Redis | None = None,
    ) -> "LLMClient":
        """Build an `LLMClient` from `config.ini`'s `[llm]` section."""
        provider = cfg.get("llm", "provider", fallback="anthropic").lower()
        model = cfg.get("llm", "model", fallback="claude-sonnet-4-6")
        temperature = _get_optional_float(cfg, "llm", "temperature")
        max_tokens = _get_optional_int(cfg, "llm", "max_tokens")
        top_p = _get_optional_float(cfg, "llm", "top_p")
        top_k = _get_optional_int(cfg, "llm", "top_k")
        timeout = _get_optional_float(cfg, "llm", "timeout")
        max_retries = cfg.getint("llm", "max_retries", fallback=2)
        cache_enabled = cfg.getboolean("llm", "cache_enabled", fallback=True)
        cache_ttl = cfg.getint("llm", "cache_ttl_seconds", fallback=DEFAULT_CACHE_TTL_SECONDS)

        chat_model = _build_chat_model(
            provider=provider,
            model=model,
            temperature=temperature,
            max_tokens=max_tokens,
            top_p=top_p,
            top_k=top_k,
            timeout=timeout,
            max_retries=max_retries,
        )
        return cls(
            chat_model,
            redis_client=redis_client,
            cache_ttl_seconds=cache_ttl,
            cache_enabled=cache_enabled,
        )

    def invoke(self, messages: Sequence[BaseMessage], **kwargs: Any) -> AIMessage:
        """Invoke the chat model, returning the cached response if present."""
        messages_list = list(messages)
        model_name = _model_identity(self.chat_model)

        cache_key = self._cache_key(messages_list, kwargs) if self._cache_enabled else None
        if cache_key is not None:
            cached = self._cache_get(cache_key)
            if cached is not None:
                log.info("[CACHED] LLM Response", model=model_name, response=cached.content)
                return cached

        response = self.chat_model.invoke(messages_list, **kwargs)
        if not isinstance(response, AIMessage):
            # LangChain chat models always return AIMessage, but guard anyway.
            response = AIMessage(content=getattr(response, "content", str(response)))

        log.info(
            "[API] LLM Response",
            model=model_name,
            response=response.content,
        )

        if cache_key is not None:
            self._cache_set(cache_key, response)

        return response

    def bind_tools(self, tools: Sequence[Any], **kwargs: Any) -> "LLMClient":
        """Return a new `LLMClient` whose underlying model has tools bound.

        The bind kwargs participate in the cache key so tool changes do
        not collide with prior cached responses.
        """
        bound_model = self.chat_model.bind_tools(list(tools), **kwargs)  # type: ignore[attr-defined]
        clone = LLMClient(
            bound_model,  # type: ignore[arg-type]
            redis_client=self._redis,
            cache_ttl_seconds=self._ttl,
            cache_enabled=self._cache_enabled,
        )
        clone._bound_kwargs = {
            **self._bound_kwargs,
            "tools": [_tool_identity(t) for t in tools],
            **{k: _jsonable(v) for k, v in kwargs.items()},
        }
        return clone

    # --- Private helpers ------------------------------------------------

    def _cache_key(self, messages: list[BaseMessage], call_kwargs: dict[str, Any]) -> str:
        payload = {
            "model": _model_identity(self.chat_model),
            "params": _model_params(self.chat_model),
            "bound": self._bound_kwargs,
            "kwargs": {k: _jsonable(v) for k, v in call_kwargs.items()},
            "messages": [_message_fingerprint(m) for m in messages],
        }
        blob = json.dumps(payload, sort_keys=True, separators=(",", ":")).encode("utf-8")
        digest = hashlib.sha256(blob).hexdigest()
        return f"{CACHE_KEY_PREFIX}{digest}"

    def _cache_get(self, key: str) -> AIMessage | None:
        if self._redis is None:
            return None
        try:
            raw = self._redis.get(key)
        except redis.RedisError as e:
            log.warning("llm cache get failed", err=str(e))
            return None
        if not raw:
            return None
        try:
            obj = lc_loads(raw)
        except Exception as e:
            logging.getLogger(__name__).warning("llm cache decode failed: %s", e)
            return None
        return obj if isinstance(obj, AIMessage) else None

    def _cache_set(self, key: str, message: AIMessage) -> None:
        if self._redis is None:
            return
        try:
            self._redis.setex(key, self._ttl, lc_dumps(message))
        except redis.RedisError as e:
            log.warning("llm cache set failed", err=str(e))


# --- Module-level helpers -----------------------------------------------


def _build_chat_model(
    *,
    provider: str,
    model: str,
    temperature: float | None,
    max_tokens: int | None,
    top_p: float | None,
    top_k: int | None,
    timeout: float | None,
    max_retries: int,
) -> BaseChatModel:
    if provider == "anthropic":
        kwargs: dict[str, Any] = {
            "model": model,
            "max_retries": max_retries,
        }
        if temperature is not None:
            kwargs["temperature"] = temperature
        if max_tokens is not None:
            kwargs["max_tokens"] = max_tokens
        if top_p is not None:
            kwargs["top_p"] = top_p
        if top_k is not None:
            kwargs["top_k"] = top_k
        if timeout is not None:
            kwargs["timeout"] = timeout
        return ChatAnthropic(**kwargs)
    raise ValueError(f"Unsupported llm.provider: {provider}")


def _model_identity(chat_model: BaseChatModel) -> str:
    return getattr(chat_model, "model", chat_model.__class__.__name__)


def _model_params(chat_model: BaseChatModel) -> dict[str, Any]:
    return {
        "temperature": getattr(chat_model, "temperature", None),
        "max_tokens": getattr(chat_model, "max_tokens", None),
        "top_p": getattr(chat_model, "top_p", None),
        "top_k": getattr(chat_model, "top_k", None),
        "timeout": getattr(chat_model, "default_request_timeout", None),
        "max_retries": getattr(chat_model, "max_retries", None),
    }


def _message_fingerprint(message: BaseMessage) -> dict[str, Any]:
    """Stable, JSON-safe fingerprint of a message for cache keying."""
    fp: dict[str, Any] = {
        "type": message.type,
        "content": _jsonable(message.content),
    }
    name = getattr(message, "name", None)
    if name:
        fp["name"] = name
    tool_calls = getattr(message, "tool_calls", None)
    if tool_calls:
        fp["tool_calls"] = [
            {"name": tc.get("name"), "args": tc.get("args")} for tc in tool_calls
        ]
    tool_call_id = getattr(message, "tool_call_id", None)
    if tool_call_id:
        # IDs are non-deterministic; include only the presence/shape.
        fp["has_tool_call_id"] = True
    return fp


def _tool_identity(tool: Any) -> str:
    if isinstance(tool, dict):
        return tool.get("name") or tool.get("type") or json.dumps(tool, sort_keys=True, default=str)
    return getattr(tool, "name", None) or getattr(tool, "__name__", repr(tool))


def _jsonable(value: Any) -> Any:
    try:
        json.dumps(value)
        return value
    except TypeError:
        return repr(value)


def _get_optional_int(
    cfg: configparser.ConfigParser,
    section: str,
    key: str,
) -> int | None:
    if not cfg.has_option(section, key):
        return None
    raw = cfg.get(section, key, fallback="").strip()
    if raw == "":
        return None
    return int(raw)


def _get_optional_float(
    cfg: configparser.ConfigParser,
    section: str,
    key: str,
) -> float | None:
    if not cfg.has_option(section, key):
        return None
    raw = cfg.get(section, key, fallback="").strip()
    if raw == "":
        return None
    return float(raw)
