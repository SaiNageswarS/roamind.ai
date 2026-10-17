# Roamind

A self-hosted, multi-user personal agent in Slack, organised around your goals, with a job application assistant on the web.

A personal open-source project, designed and built by [Sai Nageswar Satchidanand](https://github.com/SaiNageswarS) for personal use.

## Features

Novelty is rated against other self-hosted personal agents:
- ★★★: not found in comparable projects;
- ★★: a known idea with a distinct twist;
- ★: a solid, common feature.

| Feature | What it does | Novelty |
|---|---|---|
| **A channel per goal** | Say you want to build, learn or achieve something, and it becomes a Goal note in your memory. Roamind then opens a private Slack channel for it. Everything said there is about that goal, with no @mention needed. Invite someone to share the channel; each of you keeps the goal in your own memory. | ★★★ |
| **Daily exercises that adapt** | Each learning goal gets one exercise every morning, drafted from the goal, your stated preferences and your recent exercises. Your answer is graded in its thread by the same Claude session that wrote the exercise, and the results set the next one's topic and difficulty. The next exercise waits until you've answered. | ★★★ |
| **News ranked by a rubric per interest** | A daily digest from curated RSS/Atom feeds. Every interest has its own rubric in prose, for example positivity for living well or novelty for AI research. Claude judges each interest's items against it separately. A story already sent in the last 48 hours is left out, even from another source. | ★★★ |
| **Job applications, drafted daily** | Upload a resume and name your roles and regions in the web app. Once a day, at a time of its own for each user, Roamind searches applicant tracking sites on the web, judges each new posting's fit against the resume, and writes a tailored CV, a cover letter and an answer sheet for the form's questions. You can also paste links to postings you found, and get the same for each within the quarter-hour. You review them and apply yourself; new ones from the search come only once the last ones are reviewed. | ★★ |
| **Feeds can't steer the agent** | The model that ranks third-party feed text runs with no tools, no memory and no saved session, in a throwaway folder. A prompt injection in a feed can only change a score. | ★★ |
| **Interests the agent curates** | Ask in chat to follow, mute or add a news interest. Through Roamind's own MCP server, the agent adds new interests with feeds it has checked and a rubric it writes. Your 👍/👎 reactions and replies to a digest weigh its interests in later ones. | ★★ |
| **Memory with evidence** | Facts are typed, linked Markdown notes: Person, Place, Preference, Event, Goal, Progress, Task and Exercise. Each fact records how it was learned (stated, inferred, observed or corrected), from where and when. Read and edit them like any files. | ★★ |
| **Claude Code is the agent** | Every turn is `claude -p`, with web search, files, PDFs, images and a shell. A Slack thread is one resumable Claude session, and the conversation lives only in Claude's own session files. | ★★ |
| **A sandboxed workspace per user** | Each user has a private, persistent folder where Claude can write files and run commands. Files you share in Slack land in it, and the shell runs in a bubblewrap sandbox. | ★★ |
| **Slack is the main interface** | Threads are conversations, channels are goals, and the workspace is the identity. There's no sign-up or app to install. Only the job assistant lives in a web app. | ★★ |
| **Durable runs** | Every message, scheduled job and new-user setup is a [Hatchet](https://hatchet.run) workflow, with retries, per-user ordering, and runs you can trace and replay. | ★ |
| **Local memory search** | Memory search uses Qwen3 embedding and re-ranking models on your own GPU, so notes never leave the machine for search. | ★ |
| **Streamed answers with citations** | Replies stream into the thread as Claude writes them, with numbered sources. | ★ |
| **One-machine deployment** | process-compose and a systemd unit run everything; `task init` prepares the database and memory, and Tailscale exposes only the Slack webhook. | ★ |

## Security

- **Tools per feature.** Each kind of turn gets only the tools it needs:

  | Turn | Tools |
  |---|---|
  | Chat, exercise drafting | Web, files and shell in the user's workspace, plus their memory |
  | News ranking (reads third-party feed text) | **None**: no tools, no memory, a throwaway directory, no saved session |
  | Job search on the web | WebSearch and WebFetch only: no files, shell or memory; blocked domains are denied |
  | Job ranking and tailoring (read posting text) | **None**, as for news ranking |

- **Sandboxed shell.** Bash runs in a bubblewrap sandbox that sees only the user's workspace: no home directory, other users, secrets or local ports. Nothing runs unsandboxed.
- **Isolated users.** Each user has their own workspace, memory database and config. Memory and Roamind's MCP server are pinned to the turn's user.
- **Safe fetching.** The feed and job-posting readers connect only to public addresses and read at most 8 MB.
- **Small public surface.** Only the Slack webhook path is public, via Tailscale Funnel. Dashboards are tailnet-only, for listed logins. The web app is hosted on Firebase; the worker only connects out to it.
- **No accounts.** Identity is the member's Slack-verified email. Only your workspace's members can use the agent. Web users sign in with Google, and their verified email is their identity.
- **Nothing is submitted for you.** The job assistant drafts applications; it never fills in or submits a form.

## Why Slack

Slack is the main channel, by design:

- **Threads** are conversations: one resumable Claude session each.
- **Channels** are goals; invite someone to share one.
- **The workspace** is the identity: no sign-up or login.
- **Apps, notifications, search and file sharing** come for free.

The job assistant is the exception: reviewing several CVs and cover letters is easier on a web page, and job seekers needn't join the Slack workspace.

## How it works

```
Slack ─▶ Tailscale Funnel ─▶ Hatchet ─▶ worker ─▶ claude -p ─▶ reply streamed into the thread
                                                     └ memory: bm mcp (per user)
web app (Firebase) ◀─▶ Firestore ◀─ worker: daily job hunts ─▶ ATS sites
```

Details: [docs/architecture.md](docs/architecture.md); the web app's data contract: [docs/web.md](docs/web.md).

## Design principles of the code

1. **Used the task runner [go-task](https://taskfile.dev) to separate application logic from environment setup.** `task init` creates the tables, loads the seed data and refreshes every user's Basic Memory config in plain shell (`sqlite3`, `jq`, `cp`), safe to repeat. The application code never initializes its environment.
2. **Separated long-running services from one-shot tasks.** process-compose runs only the services (Caddy, the two model servers, the worker), after a one-shot `init`. Every build and setup command is a Taskfile task, and a systemd unit starts the stack at boot.
3. **Used a layered design with one-way dependencies.** The layers are:
   - `memory` is the data layer;
   - `agent` drives Claude;
   - `channels` are where users talk: Slack, and the job assistant's web app on Firebase;
   - `connectors` pull content in: RSS/Atom feeds and job postings;
   - `worker` (Hatchet workflows) and `mcp` (the agent's tools) sit on top and orchestrate.

   Each layer is its own Go module in a `go.work` workspace, and code shared by two binaries lives in a shared module, never inside one of them.
4. **Kept all data access in one place, with one Go file per schema file.** `memory/store` pairs each `sql-schemas/0N_*.sql` with a Go file holding that file's table models and queries (`users.go`, `goals.go`, `interests.go`, …). `memory/store/notes` holds the users' notes on disk. Workflows and MCP tools hold only input checks and business rules, never SQL.
5. **Kept data as data.**
   - Table definitions, seed data (plain `INSERT` statements), note schemas and Basic Memory's settings are files in `memory/`.
   - They are read from disk rather than embedded in binaries or hard-coded as Go structs.
   - The rubrics stay Markdown files, read by the seed through `sqlite3`'s `readfile`.
6. **Modelled per-user setup as an independent Hatchet workflow with explicit task dependencies.**
   - `user-setup` runs `memory-project → bm-setup → news-channel`.
   - It runs as a top-level run, so every setup can be monitored on its own.
   - It is started asynchronously whenever a user isn't set up, and nothing waits on it, so no turn or worker slot is ever blocked.
7. **Made every workflow task idempotent and serialized.**
   - Each task is safe to retry, and repeating a finished task changes nothing.
   - Concurrency keys serialize runs per user or thread, and a duplicate setup started mid-flight is cancelled.
   - State is recorded before side effects, so a retry never creates a second channel or post.
8. **Preferred upstream built-ins over wrappers.**
   - `claude -p` owns the tool-calling loop, and Basic Memory serves its own MCP server pinned with `--project`.
   - Hatchet provides scheduling, retries and concurrency.
   - Roamind adds no wrapper services around them.
9. **Applied least privilege per turn and physical isolation per user.**
   - A turn gets only the tools its input warrants.
   - Text from third-party feeds and job postings is judged with no tools at all.
   - Each user has their own sandboxed workspace, memory database and config.
10. **Stored little, and stored derived values once.** Conversations live only in Claude's session files, and SQLite just maps threads to sessions. A user's memory project name is assigned once and stored, not computed per turn.
11. **Wrote prompts that give data and say what to do.** When the model got something wrong, the fix was the right data or a positive instruction rather than a prohibition. Note schemas, not the system prompt, own the rules for each note type.
12. **Tested against the real artifacts.**
    - Tests create tables from the real schema files and run the real seed SQL through `sqlite3`.
    - A drift test runs the Taskfile's `jq` filter and Go's project setup on the same config and requires identical output.
    - The database is never mocked.
13. **Handled errors by their codes.** A unique-constraint conflict is detected by SQLite's error code, not by matching error text, and retries loop rather than recurse.
14. **Kept the code small and deletable.**
    - Package names are short and say what they hold (`store`, `notes`), and every declaration has a doc comment.
    - Unused code and one-time setup code are deleted rather than kept.

## Setup

Needs an NVIDIA GPU, Tailscale, the [Hatchet CLI](https://docs.hatchet.run/reference/cli.md), [Task](https://taskfile.dev), `sqlite3`, `jq`, `bwrap`, `socat` and Basic Memory (`uv tool install basic-memory`).

```sh
cp .env.example .env          # add TS_AUTHKEY, ROAMIND_DASHBOARD_USERS
# create the Slack app from deploy/slack-app-manifest.yaml; add its tokens to .env
task hatchet-env              # Hatchet server, Slack webhook, Funnel; prints the Slack Request URL
task all vllm
process-compose up            # runs task init, then the worker
```

For the job assistant, also `pdftotext` (poppler-utils), then follow [docs/web.md](docs/web.md#setting-up-the-workers-side).

Full steps, upgrades and running at boot: [docs/deployment.md](docs/deployment.md). Run `task` for every command.

## License

[MIT](LICENSE).
