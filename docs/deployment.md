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

- **Hatchet is started by the Hatchet CLI** (`task hatchet`, i.e. `hatchet server start`, pinned to the SDK's version), outside process-compose.
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

All one-shot steps are tasks in `Taskfile.yml`, run with [Task](https://taskfile.dev) (`go install github.com/go-task/task/v3/cmd/task@v3.54.0`; `task` lists them). process-compose runs the long-lived processes (caddy, embed, rerank, worker), plus `init` once before the worker.

1. **Config.** Copy `.env.example` to `.env`.
   - Set `TS_AUTHKEY` (needed only for Caddy's first run) and `ROAMIND_DASHBOARD_USERS` (comma-separated logins, e.g. `you@example.com`).
2. **Slack app.** Create it from `deploy/slack-app-manifest.yaml` and install it. Put `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` in `.env`.
   - **Upgrading an app created from an older manifest:**
     - Add the bot scope `groups:write` under OAuth & Permissions, then reinstall the app.
     - Re-run `task webhook` so the routing expression includes private-channel messages.
     - Rebuild and restart the worker.
3. **Hatchet environment.** `task hatchet-env` does three things:
   - starts the Hatchet server;
   - registers the Slack webhook, with the routing expression `channels/slack.EventKeyExpression`;
   - opens Tailscale Funnel on the webhook path only. The tailnet policy must allow Funnel for this node.

   It prints the Slack Request URL; paste it into the Slack app under Event Subscriptions.
4. **Build.** Install the `sqlite3` CLI and `jq` (`sudo apt install sqlite3 jq`; `task init` uses them). `task all` builds `bin/caddy`, `bin/worker` and `bin/mcp`; Task skips a binary whose sources haven't changed. `task vllm` installs vLLM (pinned, on a uv-managed Python so Triton finds `Python.h`) and downloads the two search models. This needs an NVIDIA GPU. For memory, install Basic Memory's `bm` CLI on `PATH` with `uv tool install basic-memory` (tested with 0.23.2). Its first start downloads an embedding model.
5. **Run.** `process-compose up` from the repo root. It reads `process-compose.yaml` and `.env`, and runs `task init` before the worker: with the `sqlite3` CLI it creates the store's tables from `memory/sql-schemas/` and loads the curated interests with `memory/seed/interests.sql`, then brings every user's Basic Memory project up to date: the settings in `memory/basic-memory-config.json` (search models) go into its `config.json`, and the schema notes in `memory/basic-memory-schemas/` replace the project's copies that differ. New users' projects are created by the `user-setup` workflow. Every step is safe to repeat, so editing a file in `memory/` takes effect on the next restart of the whole service (`systemctl --user restart roamind`); restarting only the worker in process-compose doesn't rerun `init`. The worker refuses to start on a store without tables. Then log into `https://hatchet.<tailnet>.ts.net` as `admin@example.com` / `Admin123!!` and change the password.

   To start at boot instead, `task service` installs and enables a `roamind` systemd user service that runs `process-compose up --tui=false` from the repo root. Manage it with `systemctl --user start|stop|status roamind`, and attach with `process-compose attach`. It starts at boot without a login only when lingering is on (`sudo loginctl enable-linger $USER`).

Development commands:
- `task test` and `task vet` run in every module.
- `task init` prepares the environment from `memory/` without starting the worker.
- `task bm -- [project] <args>` runs Basic Memory's CLI against a user's project (`task bm -- project info`, `task bm -- tool search-notes …`). It sets `BASIC_MEMORY_CONFIG_DIR` for that one command only, so your own `bm` and MCP setups keep using `~/.basic-memory`.
- `task turn -- "hello"` runs one real `claude -p` turn from the terminal.
