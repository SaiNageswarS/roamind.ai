#!/usr/bin/env bash
# Runs a command with the Hatchet client environment of a Hatchet CLI profile
# (created by `hatchet server start`), so no token lives in .env.
#
#   deploy/bin/with-hatchet.sh ./bin/worker
#
# Also exports HATCHET_UPSTREAM (host:port of the Hatchet API/dashboard) for
# Caddy and the funnel script.
set -euo pipefail

profile="${HATCHET_PROFILE:-local}"
env_exports="$(hatchet profile env --name "$profile")" || {
  echo "hatchet profile '$profile' not found; run 'hatchet server start'" >&2
  exit 1
}
eval "$env_exports"

upstream="${HATCHET_CLIENT_SERVER_URL#*://}"
export HATCHET_UPSTREAM="${upstream%%/*}"

exec "$@"
