#!/usr/bin/env bash
# Roamind build and setup tasks. Long-running processes live in
# process-compose.yaml (start them with `process-compose up`); everything
# one-shot lives here.
#
#   ./build.sh <command> [args]
#
# Commands:
#   hatchet-env   start the Hatchet server, register the Slack webhook, open the Funnel
#   hatchet       start the Hatchet server only (hatchet server start)
#   webhook       create/update the Hatchet Slack webhook
#   funnel        expose only the Slack webhook path publicly via Tailscale Funnel
#   caddy         build ./bin/caddy with the Tailscale plugin
#   worker        build ./bin/worker
#   all           build caddy and worker
#   vllm          install vLLM and download the GPU search models
#   service       install + enable the roamind systemd user service, which runs
#                 process-compose at boot (systemctl --user start|stop|status roamind)
#   test | vet    run go test / go vet in every module
#   bm [project] <args>   run Basic Memory's CLI against a user's project
#                 (default: the only project), e.g. ./build.sh bm project info
#   turn <msg>    run one agent turn from the terminal
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

ENV_FILE=".env"
MODULES=(agent channels worker memory)
WITH_HATCHET="deploy/bin/with-hatchet.sh"

HATCHET_VERSION="v0.110.22" # keep in step with the Go SDK in worker/go.mod
CADDY_VERSION="v2.11.7"
CADDY_TAILSCALE="de41b249af4f"
XCADDY_VERSION="v0.4.7"
VLLM_VERSION="0.30.0"
SEARCH_MODELS=(Qwen/Qwen3-Embedding-0.6B Qwen/Qwen3-Reranker-0.6B)

load_env() {
  if [[ -f "$ENV_FILE" ]]; then
    set -a
    # shellcheck disable=SC1090
    . "$ENV_FILE"
    set +a
  fi
}

log() { printf '\033[1m==> %s\033[0m\n' "$*"; }

cmd_hatchet() {
  log "starting Hatchet server ($HATCHET_VERSION)"
  hatchet server start --tag "$HATCHET_VERSION" --pull-policy missing --profile "${HATCHET_PROFILE:-local}"
  "$WITH_HATCHET" sh -c 'until curl -sf "$HATCHET_CLIENT_SERVER_URL/api/ready" >/dev/null; do sleep 2; done'
  log "Hatchet ready"
}

cmd_webhook() {
  if [[ -z "${SLACK_SIGNING_SECRET:-}" ]]; then
    log "SLACK_SIGNING_SECRET not set; skipping Slack webhook"
    return
  fi
  log "registering Slack webhook"
  "$WITH_HATCHET" go run ./worker/cmd/setup-webhook
}

cmd_funnel() {
  log "opening Tailscale Funnel for the Slack webhook path"
  # shellcheck disable=SC2016
  "$WITH_HATCHET" bash -c '
    set -euo pipefail
    webhooks="/api/v1/stable/tenants/${HATCHET_CLIENT_TENANT_ID}/webhooks"
    tailscale funnel --bg --https=443 --set-path "$webhooks" "http://${HATCHET_UPSTREAM}${webhooks}"
    host="$(tailscale status --json | sed -n "s/.*\"DNSName\": *\"\([^\"]*\)\.\".*/\1/p" | head -1)"
    echo "Slack Request URL: https://${host}${webhooks}/slack"
    tailscale funnel status'
}

cmd_hatchet_env() {
  cmd_hatchet
  cmd_webhook
  cmd_funnel
}

cmd_caddy() {
  log "building bin/caddy ($CADDY_VERSION + caddy-tailscale)"
  go run "github.com/caddyserver/xcaddy/cmd/xcaddy@$XCADDY_VERSION" build "$CADDY_VERSION" \
    --with "github.com/tailscale/caddy-tailscale@$CADDY_TAILSCALE" --output bin/caddy
}

cmd_worker() {
  log "building bin/worker"
  go build -o bin/worker ./worker/cmd/worker
}

cmd_vllm() {
  log "installing vLLM $VLLM_VERSION"
  # A uv-managed Python ships the headers Triton compiles against.
  uv tool install "vllm==$VLLM_VERSION" --python 3.12 --managed-python
  for m in "${SEARCH_MODELS[@]}"; do
    log "downloading $m"
    "$(uv tool dir)/vllm/bin/hf" download "$m" >/dev/null
  done
}

# process-compose runs in the foreground without its TUI; systemd is the
# daemon (Linger keeps the user manager up from boot). Attach to it with
# `process-compose attach`.
cmd_service() {
  local unit="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/roamind.service"
  log "writing $unit"
  mkdir -p "$(dirname "$unit")"
  cat >"$unit" <<UNIT
[Unit]
Description=Roamind (process-compose: caddy, embed, rerank, worker)
After=network-online.target

[Service]
Type=simple
WorkingDirectory=$ROOT
Environment=PATH=$PATH
ExecStart=$(command -v process-compose) up --tui=false
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
UNIT
  systemctl --user daemon-reload
  systemctl --user enable roamind.service
  [[ "$(loginctl show-user "$USER" -p Linger --value)" == yes ]] ||
    log "enable boot start without login: sudo loginctl enable-linger $USER"
}

cmd_all() {
  cmd_caddy
  cmd_worker
}

each_module() {
  for m in "${MODULES[@]}"; do (cd "$m" && go "$@" ./...); done
}

cmd_bm() {
  local users="${ROAMIND_DATA_DIR:-./data}/memory/users" project=""
  if [[ $# -gt 0 && -d "$users/$1" ]]; then
    project="$1"
    shift
  else
    local all=()
    [[ -d "$users" ]] && mapfile -t all < <(ls "$users")
    if [[ ${#all[@]} -ne 1 ]]; then
      echo "usage: ./build.sh bm <project> <bm args>; projects: ${all[*]:-none}" >&2
      exit 1
    fi
    project="${all[0]}"
  fi
  # Scoped to this command, so the user's own bm and MCP setups never see it.
  BASIC_MEMORY_CONFIG_DIR="$(cd "$users/$project/config" && pwd)" bm "$@"
}

cmd_turn() {
  go run ./agent/cmd/turn -data "${ROAMIND_DATA_DIR:-./data}" "$*"
}

usage() {
  awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
  exit "${1:-0}"
}

main() {
  [[ $# -gt 0 ]] || usage 1
  local cmd="$1"
  shift
  load_env
  case "$cmd" in
    hatchet-env) cmd_hatchet_env ;;
    hatchet) cmd_hatchet ;;
    webhook) cmd_webhook ;;
    funnel) cmd_funnel ;;
    caddy) cmd_caddy ;;
    worker) cmd_worker ;;
    all) cmd_all ;;
    vllm) cmd_vllm ;;
    service) cmd_service ;;
    test) each_module test ;;
    vet) each_module vet ;;
    bm) cmd_bm "$@" ;;
    turn) cmd_turn "$@" ;;
    -h | --help | help) usage ;;
    *)
      echo "unknown command: $cmd" >&2
      usage 1
      ;;
  esac
}

main "$@"
