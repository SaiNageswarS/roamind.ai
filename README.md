# Roamind

A multi-user personal agent. Phase 1 is chat over Slack. Slack sends events to Hatchet's Slack webhook. Each message becomes one `chat-turn` run, which drives `claude -p` and streams the reply into the Slack thread by editing a message.

```
Slack ─HTTPS─▶ Tailscale Funnel (webhook path only) ─▶ Hatchet ─event slack:chat─▶ worker: chat-turn
                                                                                         ├ identity (sha256(email)), allowlist
                                                                                         ├ thread → claude session (SQLite)
                                                                                         └ claude -p --resume … → throttled chat.update
```

## Modules (`go.work`)

| Module | What it holds |
|---|---|
| `protocol` | Packet types (onyx-style), turn input/output, snapshot accumulator |
| `agent` | `claude -p` runner and stream-json decoder; packet translation; Go port of onyx's citation processor; `cmd/turn` dev CLI |
| `channels` | SQLite store (users, threads, inbound events), email-hash identity and allowlist, Slack event types, mrkdwn renderer, streaming updater |
| `workflows` | Hatchet worker (`cmd/worker`), `chat-turn` task, `cmd/setup-webhook` |
| `memory` | Phase 2 placeholder (health check only) |

**Design rules**
- Conversation history lives only in claude's session files (`~/.claude/projects/<cwd>`). SQLite maps each thread to its session id and stores nothing else about the conversation.
- Claude always runs in `data/users/<user_id>/workspace`, so `--resume` can find the session.
- Built-in claude tools are disabled (`--tools ""`, `--strict-mcp-config`, `--setting-sources ""`).
- A thread belongs to the user who started it. Other users in that thread are asked to start their own.
- Runs in the same thread are serialized by the Hatchet concurrency key `channel:thread_ts`.

## Deployment

```
hatchet server start (Hatchet CLI)        process-compose (`process-compose up`)
  postgres + hatchet-lite                    hatchet-ready → waits for /api/ready
  dashboard/API :8889, gRPC :7078            caddy  → tailnet dashboards (hatchet.<tailnet>.ts.net)
  profile "local" (token, addresses)         funnel → public Slack webhook path only
                                             worker → chat-turn
                                             memory → placeholder
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

All one-shot steps are subcommands of `./build.sh` (run `./build.sh help` to list them). process-compose only runs the long-lived processes (caddy, worker, memory).

1. **Config.** Copy `.env.example` to `.env`.
   - Set `TS_AUTHKEY` (needed only for Caddy's first run) and `ROAMIND_DASHBOARD_USERS` (comma-separated logins, e.g. `you@example.com`).
   - Set `ROAMIND_ALLOWED_EMAILS`.
2. **Slack app.** Create it from `deploy/slack-app-manifest.yaml` and install it. Put `SLACK_BOT_TOKEN` and `SLACK_SIGNING_SECRET` in `.env`.
3. **Hatchet environment.** `./build.sh hatchet-env` does three things:
   - starts the Hatchet server;
   - registers the Slack webhook, with the routing expression `channels/slack.EventKeyExpression`;
   - opens Tailscale Funnel on the webhook path only. The tailnet policy must allow Funnel for this node.

   It prints the Slack Request URL; paste it into the Slack app under Event Subscriptions.
4. **Build.** `./build.sh all` builds `bin/caddy`, `bin/worker` and `bin/memory`.
5. **Run.** `process-compose up` from the repo root. It reads `process-compose.yaml` and `.env`. Then log into `https://hatchet.<tailnet>.ts.net` as `admin@example.com` / `Admin123!!` and change the password.

Development commands:
- `./build.sh test` and `./build.sh vet` run in every module.
- `./build.sh turn "hello"` runs one real `claude -p` turn from the terminal.

## Step-0 spike results (2026-10-10, hatchet v0.110.22)

**Verified locally against hatchet-lite:**
- The Slack `url_verification` challenge is answered by Hatchet.
- A bad `X-Slack-Signature` is rejected with 403.
- The raw Slack JSON body arrives as the task input (`slack.Envelope`).
- The event-key expression sends bot and other non-chat events to `slack:ignored:*`, so they create no run.
- An `app_mention` produces exactly one `chat-turn` run.
- `/api/ready` works as the readiness probe.
- The webhook can be created from code (`features.WebhooksClient`, source `SLACK`, HMAC on `X-Slack-Signature`).

**Not yet verified:**
- A real Slack app end to end (Slack → Funnel → Hatchet → worker → Slack).
- Caddy and caddy-tailscale joining the tailnet. The config validates, and handlers run in order: tailnet auth, then 403 for anyone not listed, then the proxy.
- Hatchet embedded mode. Not used, because its dashboard needs a per-start URL token.

**Also verified:**
- The webhook and its signing secret survive recreating the hatchet-lite container.
- Changing `SERVER_URL` invalidates existing API tokens.
- With `hatchet server start`:
  - the worker connects using the CLI profile, and a signed Slack event reaches `slack:chat`;
  - login works through a Caddy proxy under a different host name once the cookie's Domain is stripped.

**Verified over the public internet through Tailscale Funnel:**
- The dashboard, the login page, `/api/ready` and the tenant API all return 404, so only the webhook path is exposed.
- A signed Slack URL check at `…/webhooks/slack` returns the challenge, which shows `--set-path` keeps the path prefix.
- A bad signature returns 403.

**Known limitation:** Runs started from webhook events can't carry custom run metadata. To find a conversation's runs, use the run output (`thread`, `user_id`, `session_id`) rather than metadata filters.
