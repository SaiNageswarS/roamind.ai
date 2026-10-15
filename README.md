# Roamind

A self-hosted, multi-user personal agent in Slack, organised around your goals.

## Features

- **A channel per goal.** Say you want to build, learn or achieve something, and Roamind opens a private channel for it. Everything said there is about that goal, with no @mention needed.
- **Daily exercises.** Each learning goal gets one exercise every morning. Your answer is graded in its thread, and recent results set the next one's topic and difficulty.
- **News shorts.** A daily digest from curated feeds of your interests, each item scored by that interest's rubric. Your reactions and replies shape the next digest.
- **Memory you own.** Facts are typed, linked Markdown notes (Person, Goal, Task, …), each with its source and date. Read and edit them like any files.
- **Claude Code is the agent.** Every turn is `claude -p`: web search, files, PDFs, images and a shell in your own workspace.
- **Durable runs.** Every message and job is a [Hatchet](https://hatchet.run) run you can trace and replay.
- **Local search.** Memory search uses Qwen3 embedding and re-ranking models on your own GPU.

## Security

- **Tools per feature.** Each kind of turn gets only the tools it needs:

  | Turn | Tools |
  |---|---|
  | Chat, exercise drafting | Web, files and shell in the user's workspace, plus their memory |
  | News ranking (reads third-party feed text) | **None**: no tools, no memory, a throwaway directory, no saved session |

- **Sandboxed shell.** Bash runs in a bubblewrap sandbox that sees only the user's workspace: no home directory, other users, secrets or local ports. Nothing runs unsandboxed.
- **Isolated users.** Each user has their own workspace, memory database and config. Memory and Roamind's MCP server are pinned to the turn's user.
- **Safe feed fetching.** The feed reader connects only to public addresses and reads at most 8 MB.
- **Small public surface.** Only the Slack webhook path is public, via Tailscale Funnel. Dashboards are tailnet-only, for listed logins.
- **No accounts.** Identity is the member's Slack-verified email. Only your workspace's members can use the agent.

## Why Slack

Slack is the only channel, by design:

- **Threads** are conversations: one resumable Claude session each.
- **Channels** are goals; invite someone to share one.
- **The workspace** is the identity: no sign-up or login.
- **Apps, notifications, search and file sharing** come for free.

## How it works

```
Slack ─▶ Tailscale Funnel ─▶ Hatchet ─▶ worker ─▶ claude -p ─▶ reply streamed into the thread
                                                     └ memory: bm mcp (per user)
```

Details: [docs/architecture.md](docs/architecture.md).

## Setup

Needs an NVIDIA GPU, Tailscale, the [Hatchet CLI](https://docs.hatchet.run/reference/cli.md), `bwrap`, `socat` and Basic Memory (`uv tool install basic-memory`).

```sh
cp .env.example .env          # add TS_AUTHKEY, ROAMIND_DASHBOARD_USERS
# create the Slack app from deploy/slack-app-manifest.yaml; add its tokens to .env
./build.sh hatchet-env        # Hatchet server, Slack webhook, Funnel; prints the Slack Request URL
./build.sh all && ./build.sh vllm
process-compose up
```

Full steps, upgrades and running at boot: [docs/deployment.md](docs/deployment.md). Run `./build.sh help` for every command.
