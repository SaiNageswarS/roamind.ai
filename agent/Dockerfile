# syntax=docker/dockerfile:1.7
#
# Build context: the `agent/` directory.
#   docker build -f agent/Dockerfile -t <user>/roamind-agent:v1 ./agent
#
# The agent is a Redis-stream worker (no inbound network ports).

# ── uv binary ────────────────────────────────────────────────────────────────
FROM ghcr.io/astral-sh/uv:0.11.19 AS uv

# ── base image ────────────────────────────────────────────────────────────────
FROM python:3.11-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    # Tell uv to use the system Python and never download its own interpreter.
    UV_SYSTEM_PYTHON=1 \
    # Disable uv's cache inside Docker (layers handle caching instead).
    UV_NO_CACHE=1

WORKDIR /app

# ca-certificates: trust roots for outbound TLS (MongoDB Atlas, Anthropic, …).
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Bring in the uv binary from the official image.
COPY --from=uv /uv /usr/local/bin/uv

# Copy dependency manifests first for better layer caching.
COPY pyproject.toml uv.lock ./

# Install only production dependencies (no dev group) from the locked versions.
# --no-install-project: skip installing the package itself until source is present.
RUN uv sync --frozen --no-dev --no-install-project

# Copy application source and config.
COPY src/ ./src/
COPY config.ini ./

# Install the project package itself now that source is available.
RUN uv sync --frozen --no-dev

# Non-root runtime user.
RUN useradd --create-home --uid 10001 roamind \
 && chown -R roamind:roamind /app
USER roamind

ENTRYPOINT ["roamind-agent"]
