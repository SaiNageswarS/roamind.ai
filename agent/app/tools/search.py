"""External search @tool callables.

  - `web_search`      — general web search via Exa.
  - `github_search`   — GitHub repos + README/doc excerpts via Exa.
  - `news_search`     — recent news articles via Exa.
  - `research_search` — academic / research papers via Exa.
  - `finance_search`  — live equity / FX / crypto quotes via Yahoo Finance.
  - `maps_search`     — places / business search via Google Places API.

Configuration:
  - `EXA_API_KEY` env var — required for web / github / news / research.
  - `GOOGLE_MAPS_API_KEY` env var — required for `maps_search`.

The module is runnable standalone for ad-hoc smoke testing — `.env`
is loaded automatically (searched upwards from the current directory):

    # from agent/
    python -m app.tools.search web "langgraph tutorial"
    python -m app.tools.search github "gin web framework"
    python -m app.tools.search news "openai" --days 7
    python -m app.tools.search research "transformer attention"
    python -m app.tools.search finance AAPL,MSFT,BTC-USD
    python -m app.tools.search maps "coffee near Times Square" --limit 3
"""

from __future__ import annotations

import argparse
import os
import sys
from datetime import datetime, timedelta, timezone
from typing import Any

import structlog
from dotenv import load_dotenv
from langchain_core.tools import tool

from ._http import http_get_json, http_post_json

# Load .env eagerly so the module-level API-key constants below see
# values whether this file is imported by the agent or run as a script.
load_dotenv()

log = structlog.get_logger("roamind.agent.tools.search")

_MAX_RESULTS = 5

_EXA_API_KEY = os.getenv("EXA_API_KEY", "").strip() or None
_EXA_SEARCH_URL = "https://api.exa.ai/search"

_GOOGLE_MAPS_API_KEY = os.getenv("GOOGLE_MAPS_API_KEY", "").strip() or None
_GOOGLE_PLACES_TEXT_SEARCH_URL = "https://places.googleapis.com/v1/places:searchText"
_GOOGLE_PLACES_FIELD_MASK = (
    "places.id,places.displayName,places.formattedAddress,"
    "places.location,places.rating,places.types"
)


# --- Public tools -------------------------------------------------------


@tool
def web_search(query: str) -> str:
    """Search the web for general information, definitions, how-tos, and facts.

    **Use for:** definitions, how-tos, concepts, general questions, explanations.
    **Don't use for:** GitHub repos (use github_search), news (use news_search),
        academic papers (use research_search), live prices/quotes (use finance_search),
        places/addresses (use maps_search).

    Args:
        query: Free-text search query. Focused phrase works best.
    """
    return _exa_search(query, num_results=_MAX_RESULTS)


@tool
def github_search(query: str) -> str:
    """Find GitHub repositories with relevant README / documentation excerpts.

    **Use for:** finding libraries, frameworks, dev tools, open-source projects,
        or repos by capability ("library that does X in Y language").
    **Returns:** repo URL plus query-relevant README/doc excerpts.

    Args:
        query: Repo name, topic, or descriptive phrase (e.g.
            "gin web framework", "Python fuzzy string matching library").
    """
    return _exa_search(query, category="github", num_results=_MAX_RESULTS)


@tool
def news_search(query: str, days: int = 30) -> str:
    """Recent news articles on a topic.

    **Use for:** current events, breaking stories, recent headlines, announcements.
    **Don't use for:** evergreen facts (use web_search), academic findings
        (use research_search).

    Args:
        query: News topic or query.
        days: How recent results should be, in days (1-90, default 30).
    """
    days = max(1, min(int(days or 30), 90))
    since = (datetime.now(timezone.utc) - timedelta(days=days)).strftime(
        "%Y-%m-%dT%H:%M:%S.000Z"
    )
    return _exa_search(
        query,
        category="news",
        num_results=_MAX_RESULTS,
        start_published_date=since,
    )


@tool
def research_search(query: str) -> str:
    """Academic papers and research publications.

    **Use for:** scientific findings, peer-reviewed studies, literature on a topic,
        paper lookups by title or topic.
    **Don't use for:** popular-press summaries (use news_search or web_search).

    Args:
        query: Paper title, research topic, or specific question.
    """
    return _exa_search(query, category="research paper", num_results=_MAX_RESULTS)


@tool
def maps_search(query: str, limit: int = 3) -> str:
    """Find places, businesses, addresses, landmarks, and coordinates.

    **Use for:** finding businesses (restaurants, shops, services), landmarks,
    addresses, points of interest, and their coordinates / ratings.
    **Don't use for:** routing or turn-by-turn directions.

    Returns up to `limit` places with name, address, rating, primary type,
    coordinates, and a Google Maps link. Requires `GOOGLE_MAPS_API_KEY`.

    Args:
        query: Place name, business type, or address (e.g.
            "coffee near Times Square", "Eiffel Tower",
            "1600 Amphitheatre Parkway, Mountain View").
        limit: Max number of places to return (1-5).
    """
    query = (query or "").strip()
    if not query:
        return "maps_search: empty query"
    if not _GOOGLE_MAPS_API_KEY:
        return "maps_search: not configured (GOOGLE_MAPS_API_KEY missing)"
    limit = max(1, min(int(limit or 1), 5))

    return _google_places_text_search(query, limit) or "maps_search: upstream unavailable"


@tool
def finance_search(symbols: str) -> str:
    """Get live quotes for stocks, ETFs, indices, FX pairs, and crypto.

    **Use for:** current prices, market changes, 24h performance.
    **Accepts:** stock tickers (AAPL), indices (^GSPC), FX (EURUSD=X), crypto (BTC-USD).

    Returns price, change %, and currency via Yahoo Finance.

    Args:
        symbols: Comma-separated list of up to 5 ticker symbols
            (e.g. "AAPL,MSFT,BTC-USD").
    """
    raw = (symbols or "").strip()
    if not raw:
        return "finance_search: no symbols provided"
    tickers = [s.strip().upper() for s in raw.replace(";", ",").split(",") if s.strip()]
    tickers = tickers[:5]
    if not tickers:
        return "finance_search: no symbols provided"

    params = {"symbols": ",".join(tickers)}
    data = http_get_json(
        "https://query1.finance.yahoo.com/v7/finance/quote",
        params=params,
        headers={"Accept": "application/json"},
    )
    if isinstance(data, str):
        return f"finance_search: {data}"

    quotes = (
        (data.get("quoteResponse") or {}).get("result")
        if isinstance(data, dict)
        else None
    )
    if not quotes:
        return f"finance_search: no quotes for {tickers!r}"

    lines = []
    for q in quotes:
        if not isinstance(q, dict):
            continue
        symbol = q.get("symbol", "?")
        price = q.get("regularMarketPrice")
        change = q.get("regularMarketChange")
        pct = q.get("regularMarketChangePercent")
        currency = q.get("currency", "")
        name = q.get("shortName") or q.get("longName") or symbol
        lines.append(
            f"{symbol} ({name}): {price} {currency} "
            f"Δ {change} ({pct}%)"
            if price is not None
            else f"{symbol} ({name}): no quote"
        )
    if not lines:
        return f"finance_search: no quotes for {tickers!r}"
    return "\n".join(lines)


SEARCH_TOOLS = [
    web_search,
    github_search,
    news_search,
    research_search,
    finance_search,
    maps_search,
]


# --- Private helpers ----------------------------------------------------


def _exa_search(
    query: str,
    *,
    category: str | None = None,
    num_results: int = _MAX_RESULTS,
    start_published_date: str | None = None,
) -> str:
    """Call Exa /search with highlights enabled and a small text fallback."""
    query = (query or "").strip()
    if not query:
        return "search: empty query"
    if not _EXA_API_KEY:
        return "search: not configured (EXA_API_KEY missing)"

    body: dict[str, Any] = {
        "query": query,
        "type": "auto",
        "numResults": num_results,
        "contents": {
            "text": {"maxCharacters": 1500},
            "highlights": {
                "numSentences": 3,
                "highlightsPerUrl": 2,
                "query": query,
            },
        },
    }
    if category:
        body["category"] = category
    if start_published_date:
        body["startPublishedDate"] = start_published_date

    headers = {"x-api-key": _EXA_API_KEY}
    data = http_post_json(_EXA_SEARCH_URL, json_body=body, headers=headers)
    if isinstance(data, str):
        log.warning("exa error", err=data, category=category)
        return f"search: {data}"
    if not isinstance(data, dict):
        return "search: bad response"

    items = data.get("results") or []
    if not items:
        return f"search: no results for {query!r}"

    results: list[dict[str, str]] = []
    for it in items[:num_results]:
        if not isinstance(it, dict):
            continue
        title = (it.get("title") or "").strip() or query
        url = it.get("url") or ""

        highlights = [h.strip() for h in (it.get("highlights") or []) if h]
        if highlights:
            snippet = " … ".join(highlights)
        else:
            text = (it.get("text") or "").strip()
            snippet = text[:400] + ("…" if len(text) > 400 else "")

        published = (it.get("publishedDate") or "")[:10]
        if published:
            snippet = f"[{published}] {snippet}" if snippet else f"[{published}]"

        results.append({"title": title, "snippet": snippet, "url": url})

    if not results:
        return f"search: no usable results for {query!r}"
    return _format_results(results)


def _google_places_text_search(query: str, limit: int) -> str | None:
    """Call Google Places (New) Text Search."""
    headers = {
        "Content-Type": "application/json",
        "X-Goog-Api-Key": _GOOGLE_MAPS_API_KEY or "",
        "X-Goog-FieldMask": _GOOGLE_PLACES_FIELD_MASK,
    }
    body = {"textQuery": query, "maxResultCount": limit}
    data = http_post_json(_GOOGLE_PLACES_TEXT_SEARCH_URL, json_body=body, headers=headers)
    if isinstance(data, str):
        log.warning("google places error", err=data)
        return None
    if not isinstance(data, dict):
        return None

    places = data.get("places") or []
    if not places:
        return f"maps_search: no places matched {query!r}"

    results: list[dict[str, str]] = []
    for place in places[:limit]:
        if not isinstance(place, dict):
            continue
        name = (place.get("displayName") or {}).get("text") or query
        address = place.get("formattedAddress") or ""
        rating = place.get("rating")
        types = place.get("types") or []
        primary_type = types[0] if types else ""
        loc = place.get("location") or {}
        lat, lng = loc.get("latitude"), loc.get("longitude")
        place_id = place.get("id") or ""

        snippet_bits = []
        if address:
            snippet_bits.append(address)
        if rating is not None:
            snippet_bits.append(f"★ {rating}")
        if primary_type:
            snippet_bits.append(primary_type.replace("_", " "))
        if lat is not None and lng is not None:
            snippet_bits.append(f"({lat:.4f}, {lng:.4f})")

        results.append({
            "title": name,
            "snippet": " | ".join(snippet_bits),
            "url": (
                f"https://www.google.com/maps/place/?q=place_id:{place_id}"
                if place_id
                else ""
            ),
        })

    if not results:
        return f"maps_search: no usable results for {query!r}"
    return _format_results(results)


def _format_results(results: list[dict[str, str]]) -> str:
    out = []
    for i, r in enumerate(results, 1):
        title = (r.get("title") or "").strip()
        snippet = (r.get("snippet") or "").strip()
        url = (r.get("url") or "").strip()
        line = f"{i}. {title}"
        if snippet:
            line += f"\n   {snippet}"
        if url:
            line += f"\n   {url}"
        out.append(line)
    return "\n".join(out)


# --- Script entrypoint --------------------------------------------------


_TOOL_BY_NAME = {
    "web": web_search,
    "github": github_search,
    "news": news_search,
    "research": research_search,
    "finance": finance_search,
    "maps": maps_search,
}


def _main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="python -m app.tools.search",
        description="Invoke one of the search @tool callables for local testing.",
    )
    parser.add_argument(
        "tool",
        choices=sorted(_TOOL_BY_NAME),
        help="Which search tool to invoke.",
    )
    parser.add_argument(
        "query",
        help=(
            "Free-text query (or comma-separated tickers for `finance`)."
        ),
    )
    parser.add_argument(
        "--days",
        type=int,
        default=30,
        help="Lookback window for `news` (default 30).",
    )
    parser.add_argument(
        "--limit",
        type=int,
        default=3,
        help="Max places to return for `maps` (default 3).",
    )
    args = parser.parse_args(argv)

    fn = _TOOL_BY_NAME[args.tool]
    if args.tool == "news":
        payload = {"query": args.query, "days": args.days}
    elif args.tool == "maps":
        payload = {"query": args.query, "limit": args.limit}
    elif args.tool == "finance":
        payload = {"symbols": args.query}
    else:
        payload = {"query": args.query}

    # `@tool` decorators expose `.invoke(dict)` to call the underlying function.
    result = fn.invoke(payload)
    print(result)

    err_prefixes = ("search:", "maps_search:", "finance_search:")
    if isinstance(result, str) and result.startswith(err_prefixes):
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(_main())
