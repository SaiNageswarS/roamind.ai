#!/usr/bin/env bash
# Serves pgAdmin 4, a web client for the Postgres store, on loopback
# 127.0.0.1:5050, for Caddy's postgres.<tailnet>.ts.net. Started by
# process-compose; install with task pgadmin.
#
# pgAdmin runs as you and connects over the Unix socket as your role (peer
# authentication), so it needs no password, and has no login of its own:
# Caddy lets in only ROAMIND_DASHBOARD_USERS. It reaches every database, as
# you can: the operator sees every user's data. Its state is in data/pgadmin.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
data="${ROAMIND_DATA_DIR:-$ROOT/data}/pgadmin"
venv="$ROOT/deploy/pgadmin/.venv"
[[ -x "$venv/bin/pgadmin4" ]] || {
  echo "run-pgadmin: $venv not found; run task pgadmin" >&2
  exit 1
}
mkdir -p "$data"
data="$(cd "$data" && pwd)"

# The server it opens with: the local socket, as the OS user. pgAdmin asks
# for a password before connecting unless the server has a passfile, so it
# gets an empty one; peer authentication then needs none.
: >"$data/pgpass"
chmod 600 "$data/pgpass"
sock="${PGHOST:-/var/run/postgresql}"
cat >"$data/servers.json" <<JSON
{"Servers": {"1": {
  "Name": "roamind (local socket)", "Group": "Servers",
  "Host": "$sock", "Port": 5432, "MaintenanceDB": "roamind",
  "Username": "$(id -un)", "SSLMode": "prefer",
  "PassFile": "$data/pgpass"
}}}
JSON

export PGADMIN_SERVER_MODE=OFF
export PGADMIN_DATA_DIR="$data"
export CONFIG_DISTRO_FILE_PATH="$ROOT/deploy/pgadmin/config.py"
export PGADMIN_INT_PORT=5050
export HOME="$data"

# The first start creates the configuration database, so the server is
# imported once pgAdmin answers, replacing the last one so a restart adds no copy.
(
  for _ in $(seq 60); do
    curl -fs "http://127.0.0.1:$PGADMIN_INT_PORT/misc/ping" >/dev/null 2>&1 && break
    sleep 1
  done
  "$venv/bin/pgadmin4-cli" load-servers --replace "$data/servers.json" >/dev/null 2>&1 || true
) &

exec "$venv/bin/pgadmin4"
