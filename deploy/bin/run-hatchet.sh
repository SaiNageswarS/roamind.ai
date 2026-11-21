#!/usr/bin/env bash
# Starts hatchet-lite in Docker against the host's PostgreSQL, over its Unix
# socket, so Roamind and Hatchet share one Postgres instance (databases
# `hatchet` and `roamind`). Replaces `hatchet server start`, which runs a
# Postgres of its own in Docker. Safe to repeat.
#
#   deploy/bin/run-hatchet.sh <version>     e.g. v0.110.22
#
# Needs a Postgres role named like the current user (peer authentication, no
# password) that owns the database `hatchet`, whose timezone is UTC:
#   createdb hatchet && psql postgres -c "ALTER DATABASE hatchet SET timezone TO 'UTC'"
# The container runs as the current user so the socket accepts it. Its
# generated keys are in data/hatchet/config (made by the first start).
set -euo pipefail

version="${1:?usage: run-hatchet.sh <hatchet version>}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
name=roamind-hatchet
config="${ROAMIND_DATA_DIR:-$root/data}/hatchet/config"
socket="${POSTGRES_SOCKET_DIR:-/var/run/postgresql}"
mkdir -p "$config"

if docker ps --format '{{.Names}}' | grep -qx "$name"; then
  echo "$name is running"
  exit 0
fi
if docker ps -a --format '{{.Names}}' | grep -qx "$name"; then
  docker start "$name" >/dev/null
  exit 0
fi

docker run -d --name "$name" --user "$(id -u):$(id -g)" --restart unless-stopped \
  -v "$config:/config" -v "$socket:$socket" \
  -p 8889:8888 -p 7078:7077 \
  -e "DATABASE_URL=postgresql://$(id -un)@$(printf %s "$socket" | sed 's|/|%2F|g')/hatchet?sslmode=disable" \
  -e SERVER_AUTH_COOKIE_DOMAIN=localhost -e SERVER_AUTH_COOKIE_INSECURE=t \
  -e SERVER_GRPC_BIND_ADDRESS=0.0.0.0 -e SERVER_GRPC_INSECURE=t \
  -e SERVER_GRPC_BROADCAST_ADDRESS=localhost:7078 -e SERVER_GRPC_PORT=7077 \
  -e SERVER_URL=http://localhost:8889 -e SERVER_AUTH_SET_EMAIL_VERIFIED=t \
  -e SERVER_INTERNAL_CLIENT_INTERNAL_GRPC_BROADCAST_ADDRESS=localhost:7077 \
  -e LITE_STATIC_ASSET_DIR=/static-assets -e LITE_FRONTEND_BASE_PATH=/ \
  -e LITE_FRONTEND_PORT=8081 -e LITE_RUNTIME_PORT=8888 \
  "ghcr.io/hatchet-dev/hatchet/hatchet-lite:$version" ./entrypoint.sh >/dev/null
