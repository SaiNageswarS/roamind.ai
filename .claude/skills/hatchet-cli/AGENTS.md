# Hatchet CLI Agent Skills

This skill package teaches AI agents how to use the Hatchet CLI to manage workflows, workers, and runs.

## When to use these skills

Read the relevant reference document before performing any Hatchet CLI task:

- **Setting up the CLI or creating a profile** → `references/setup-cli.md`
- **Running Hatchet locally without a token (embedded mode)** → `references/local-dev-embedded.md`
- **Starting a worker** → `references/start-worker.md`
- **Triggering a workflow and waiting for results** → `references/trigger-and-watch.md`
- **Debugging a failed or stuck run** → `references/debug-run.md`
- **Replaying a run with the same or new input** → `references/replay-run.md`

## Key conventions

- For local development, prefer embedded mode: no token, account, or server needed (`references/local-dev-embedded.md`).
- Always specify a profile with `-p HATCHET_PROFILE` unless a default profile is set. Embedded mode needs no profile.
- Use `-o json` for machine-readable output when parsing responses.
- Write workflow input to a temp file (e.g. `/tmp/hatchet-input-$(date +%s)-$$.json`) to avoid collisions.
- Clean up temp files after use.
