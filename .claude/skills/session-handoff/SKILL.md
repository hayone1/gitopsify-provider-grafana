---
name: session-handoff
description: Pick up work from HANDOFF.md at the start of a session and update it at the end. Use at the start of every session in gitopsify-provider-grafana, when the user says "continue", "pick up where you left off" or "what is the status", before context runs out, and always before you finish a session, even when the work is unfinished.
---

# Session handoff

`HANDOFF.md` at the repository root is the memory between sessions. Any agent
(or person) must be able to continue from it alone.

## At the start of a session

1. Read `HANDOFF.md` completely, and `../gitopsify/HANDOFF.md` if the work
   touches the SDK or gitopsify (this repository builds against that checkout
   until the SDK is tagged).
2. Check what it says is still true: `git status`, `git log --oneline -5`,
   `gh run list --limit 5`, `docker ps --filter name=grafana` (local test
   Grafana), and that no Hetzner test machine was left running (see
   `../gitopsify/.claude/skills/session-handoff/SKILL.md` for the command; test
   machines carry the label `gitopsify-e2e-runner`).
3. Tell the user in one or two sentences what is open, then continue.

## At the end of a session

Update `HANDOFF.md` before your final message, even when the work is
unfinished. Keep it short and factual; replace stale content instead of
appending. Keep current: the date (absolute), the state (what works, how it was
verified: `make test`, the live test and its result), Grafana and upstream
versions tested, environment (what is running, what costs money, where
credentials live, never their values), open items in priority order, and known
problems (Grafana or Terraform provider behaviour that shapes the mapping).

Before writing it: make sure the changes were reviewed by a fresh-context
reviewer subagent and findings were fixed or recorded; run `make lint test`;
make sure no test machine is left running. Never write secret values into
`HANDOFF.md`.
