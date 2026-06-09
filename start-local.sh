#!/usr/bin/env bash
# start-local.sh — Start Redis if not running, then run the Roamind agent interactively.
#
# Usage:
#   ./start-local.sh
#
# Each invocation gets its own session ID (used as the conversation user_id),
# so multiple terminals run independent conversations against the same Redis.
#
# Requires: redis-server, uv
# Reads:    agent/.env (or root .env via dotenv in the agent)

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Start Redis only if not already running ────────────────────────────────────
if redis-cli ping > /dev/null 2>&1; then
    echo "Redis already running."
else
    echo "Starting Redis..."
    redis-server --daemonize yes --logfile /tmp/roamind-redis.log --pidfile /tmp/roamind-redis.pid

    for i in $(seq 1 10); do
        if redis-cli ping > /dev/null 2>&1; then
            echo "Redis is ready."
            break
        fi
        if [ "$i" -eq 10 ]; then
            echo "ERROR: Redis did not become ready in time." >&2
            exit 1
        fi
        sleep 1
    done
fi

# ── Run agent (stdin mode) ─────────────────────────────────────────────────────
# USER_ID is fixed — tied to long-term memory (MongoDB profile, habits, etc.).
# CONVERSATION_ID is unique per terminal session — isolates short-term history.
CONVERSATION_ID="$(uuidgen | tr '[:upper:]' '[:lower:]')"
echo "User:         05b5c769"
echo "Conversation: $CONVERSATION_ID"
echo "Starting Roamind agent (stdin mode)..."
cd "$ROOT_DIR/agent"
CLI_USER_ID="05b5c769" CONVERSATION_ID="$CONVERSATION_ID" uv run roamind-agent --mode stdin
