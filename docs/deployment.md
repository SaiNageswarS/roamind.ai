# Deployment

How Roamind is run and set up. For an overview, see the [README](../README.md).

## Processes

```
hatchet server start (Hatchet CLI)        process-compose (`process-compose up`)
  postgres + hatchet-lite                    hatchet-ready → waits for /api/ready
  dashboard/API :8889, gRPC :7078            caddy  → tailnet dashboards (hatchet.<tailnet>.ts.net)
  profile "local" (token, addresses)         funnel → public Slack webhook path only
                                             embed/rerank → vLLM search models (GPU, loopback)
                                             worker → chat-turn, goal-channels, learning-exercise(s), news-*
```

- **Hatchet is started by the Hatchet CLI** (`./build.sh hatchet`, i.e. `hatchet server start`, pinned to the SDK's version), outside process-compose.
  - It creates the `local` profile. `deploy/bin/with-hatchet.sh` loads that profile (`hatchet profile env`) for the worker, Caddy and the `webhook`/`funnel` commands, so no token goes in `.env`.
  - The ports come from the profile, so the auto-picked ports (8889/7078 here) are fine.
- **Dashboards** are served by Caddy built with [caddy-tailscale](https://github.com/tailscale/caddy-tailscale).
  - Each dashboard joins the tailnet as its own device, e.g. `hatchet`, and so gets its own name and certificate (`hatchet.<tailnet>.ts.net`). It isn't a subdomain of this machine.
  - They're reachable anywhere you have the Tailscale app, including your phone.
  - Only the Tailscale logins in `ROAMIND_DASHBOARD_USERS` get in, since the tailnet may be shared.
  - Caddy strips `Domain=localhost` from Hatchet's login cookie, so you can log in under the tailnet name.
  - To add a dashboard, copy the hatchet site block in `deploy/caddy/Caddyfile` with a new device name.
  - Path-based routing doesn't work for Hatchet: its page uses `<base href="/">` and calls `/api` from the site root.
- **The only public endpoint** is Hatchet's webhook path, exposed through Tailscale Funnel.
- **Caution:** `hatchet server start` binds its ports on all interfaces (0.0.0.0), so tailnet and LAN peers can reach `:8889` directly, bypassing Caddy. Change the default admin password, and firewall those ports, e.g. `sudo ufw deny in on tailscale0 to any port 8889,7078 proto tcp`.

## Setup

All one-shot steps are subcommands of `./build.sh` (run `./build.sh help` to list them). process-compose only runs the long-lived processes (caddy, embed, rerank, worker).

1. **Config.** Copy `.env.example` to `.env`.
   - Set `TS_AUTHKEY` (needed only for Caddy's first run) and `ROAMIND_DASHBOARD_USERS` (comma-separated logins, e.g. `you@example.com`).
2. **Slack app.** Create it from `deploy/slack-app-manifest.yaml` and install it. Put `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` in `.env`.
   - **Upgrading an app created from an older manifest:**
     - Add the bot scope `groups:write` under OAuth & Permissions, then reinstall the app.
     - Re-run `./build.sh webhook` so the routing expression includes private-channel messages.
     - Rebuild and restart the worker.
3. **Hatchet environment.** `./build.sh hatchet-env` does three things:
   - starts the Hatchet server;
   - registers the Slack webhook, with the routing expression `channels/slack.EventKeyExpression`;
   - opens Tailscale Funnel on the webhook path only. The tailnet policy must allow Funnel for this node.

   It prints the Slack Request URL; paste it into the Slack app under Event Subscriptions.
4. **Build.** `./build.sh all` builds `bin/caddy`, `bin/worker` and `bin/mcp`. `./build.sh vllm` installs vLLM (pinned, on a uv-managed Python so Triton finds `Python.h`) and downloads the two search models. This needs an NVIDIA GPU. For memory, install Basic Memory's `bm` CLI on `PATH` with `uv tool install basic-memory` (tested with 0.23.2). Its first start downloads an embedding model.
5. **Run.** `process-compose up` from the repo root. It reads `process-compose.yaml` and `.env`. Then log into `https://hatchet.<tailnet>.ts.net` as `admin@example.com` / `Admin123!!` and change the password.

   To start at boot instead, `./build.sh service` installs and enables a `roamind` systemd user service that runs `process-compose up --tui=false` from the repo root. Manage it with `systemctl --user start|stop|status roamind`, and attach with `process-compose attach`. It starts at boot without a login only when lingering is on (`sudo loginctl enable-linger $USER`).

Development commands:
- `./build.sh test` and `./build.sh vet` run in every module.
- `./build.sh bm [project] <args>` runs Basic Memory's CLI against a user's project (`./build.sh bm project info`, `./build.sh bm tool search-notes …`). It sets `BASIC_MEMORY_CONFIG_DIR` for that one command only, so your own `bm` and MCP setups keep using `~/.basic-memory`.
- `./build.sh turn "hello"` runs one real `claude -p` turn from the terminal.
