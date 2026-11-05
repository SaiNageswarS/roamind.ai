#!/usr/bin/env bash
# Starts QuestDB on loopback only, with its data in <ROAMIND_DATA_DIR>/questdb.
# Roamind reads and writes over QWP, QuestDB's WebSocket protocol, on the HTTP
# port (9000), which also serves the web console and REST API; Caddy puts the
# console on the tailnet. QWP needs line protocol over HTTP, so that stays
# on. Postgres wire, line protocol over TCP and UDP, and telemetry are off.
#
# Usage: run-questdb.sh <questdb home> [questdb.sh start options]; -n keeps it
# in the foreground, as process-compose runs it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

home="$1"
shift

export QDB_HTTP_BIND_TO=127.0.0.1:9000
export QDB_HTTP_MIN_NET_BIND_TO=127.0.0.1:9003
export QDB_PG_ENABLED=false
export QDB_LINE_TCP_ENABLED=false
export QDB_LINE_UDP_ENABLED=false
export QDB_TELEMETRY_ENABLED=false

exec "$home/bin/questdb.sh" start "$@" -d "$(realpath -m "${ROAMIND_DATA_DIR:-./data}/questdb")"
