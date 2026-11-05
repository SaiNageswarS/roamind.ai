#!/usr/bin/env bash
# Starts QuestDB locked down: every port on loopback, line protocol off, HTTP
# (the web console and REST API) read-only, telemetry off, and Postgres wire
# behind ROAMIND_QUESTDB_PASSWORD (user admin) plus a read-only user (user)
# behind ROAMIND_QUESTDB_READONLY_PASSWORD. Its data goes in
# <ROAMIND_DATA_DIR>/questdb.
#
# Usage: run-questdb.sh <questdb home> [questdb.sh start options]; -n keeps it
# in the foreground, as process-compose runs it.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

home="$1"
shift
pw="${ROAMIND_QUESTDB_PASSWORD:-}"
ro="${ROAMIND_QUESTDB_READONLY_PASSWORD:-}"
if [[ -z "$pw" || "$pw" == quest ]]; then
  echo "run-questdb: set ROAMIND_QUESTDB_PASSWORD in .env (not QuestDB's default, quest)" >&2
  exit 1
fi
if [[ -z "$ro" || "$ro" == quest || "$ro" == "$pw" ]]; then
  echo "run-questdb: set ROAMIND_QUESTDB_READONLY_PASSWORD in .env, different from ROAMIND_QUESTDB_PASSWORD" >&2
  exit 1
fi

export QDB_HTTP_BIND_TO=127.0.0.1:9000
export QDB_HTTP_MIN_NET_BIND_TO=127.0.0.1:9003
export QDB_PG_NET_BIND_TO=127.0.0.1:8812
export QDB_HTTP_SECURITY_READONLY=true
export QDB_LINE_TCP_ENABLED=false
export QDB_LINE_HTTP_ENABLED=false
export QDB_LINE_UDP_ENABLED=false
export QDB_TELEMETRY_ENABLED=false
export QDB_PG_PASSWORD="$pw"
export QDB_PG_READONLY_USER_ENABLED=true
export QDB_PG_READONLY_PASSWORD="$ro"

exec "$home/bin/questdb.sh" start "$@" -d "$(realpath -m "${ROAMIND_DATA_DIR:-./data}/questdb")"
