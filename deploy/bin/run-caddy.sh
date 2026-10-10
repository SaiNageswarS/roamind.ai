#!/usr/bin/env bash
# Starts Caddy for the tailnet dashboards. Turns ROAMIND_DASHBOARD_USERS
# (comma-separated Tailscale logins) into the CEL list the Caddyfile matches
# against, rejecting anything that is not a plain login, then runs Caddy with
# the Hatchet profile environment (for HATCHET_UPSTREAM).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."

items=()
IFS=',' read -ra users <<<"${ROAMIND_DASHBOARD_USERS:-}"
for u in "${users[@]}"; do
  u="$(tr -d "[:space:]'\"" <<<"$u")"
  [[ -z "$u" ]] && continue
  if [[ ! "$u" =~ ^[A-Za-z0-9._%+@-]+$ ]]; then
    echo "run-caddy: invalid login in ROAMIND_DASHBOARD_USERS: $u" >&2
    exit 1
  fi
  items+=("'$u'")
done
if [[ ${#items[@]} -eq 0 ]]; then
  echo "run-caddy: ROAMIND_DASHBOARD_USERS is empty; every dashboard request will get 403" >&2
fi
ROAMIND_DASHBOARD_USERS_CEL="$(IFS=,; echo "${items[*]:-}")"
export ROAMIND_DASHBOARD_USERS_CEL

exec deploy/bin/with-hatchet.sh ./bin/caddy run --config deploy/caddy/Caddyfile --adapter caddyfile
