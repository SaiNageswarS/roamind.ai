# syntax=docker/dockerfile:1.7
#
# Build context: the `agent/` directory.
#   docker build -f agent/Dockerfile -t <user>/roamind-agent:v1 ./agent
#
# The agent is a Redis-stream worker (no inbound network ports).

FROM python:3.11-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_NO_CACHE_DIR=1

WORKDIR /app

# - build-essential: compiles native wheels (pydantic-core etc.)
# - ca-certificates: trust roots for outbound TLS (MongoDB Atlas,
#   Anthropic API, etc.). python:3.11-slim usually ships these but we
#   install + update explicitly to guarantee a fresh bundle.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
 && update-ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Install Python dependencies first for better layer caching.
COPY pyproject.toml ./
COPY app/ ./app/
COPY config.ini ./

RUN pip install . \
 && apt-get purge -y --auto-remove build-essential \
 && rm -rf /root/.cache /var/lib/apt/lists/*

# Non-root runtime user.
RUN useradd --create-home --uid 10001 roamind \
 && chown -R roamind:roamind /app
USER roamind

ENTRYPOINT ["python", "-m", "app.main"]
