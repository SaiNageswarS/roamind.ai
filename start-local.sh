#!/usr/bin/env bash
# start-local.sh — Start Redis and run the Roamind agent interactively.
#
# Usage:
#   ./start-local.sh
#
# Requires: redis-server, uv
# Reads:    agent/.env (or root .env via dotenv in the agent)

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── Start Redis ────────────────────────────────────────────────────────────────
echo "Starting Redis..."
redis-server --daemonize yes --logfile /tmp/roamind-redis.log --pidfile /tmp/roamind-redis.pid

# Wait until Redis is ready.
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

# ── Trap: stop Redis on exit ───────────────────────────────────────────────────
cleanup() {
    echo ""
    echo "Stopping Redis..."
    if [ -f /tmp/roamind-redis.pid ]; then
        kill "$(cat /tmp/roamind-redis.pid)" 2>/dev/null || true
        rm -f /tmp/roamind-redis.pid
    fi
}
trap cleanup EXIT

# ── Run agent (stdin mode) ─────────────────────────────────────────────────────
echo "Starting Roamind agent (stdin mode)..."
cd "$ROOT_DIR/agent"
uv run roamind-agent --mode stdin
