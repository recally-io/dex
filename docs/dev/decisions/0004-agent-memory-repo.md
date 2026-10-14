# 0004: Memory in the Agent Memory Repo format, with dreaming

Status: Accepted (2026-10-14)

## Context

Each chat starts without the earlier chats, so the user repeats preferences
and facts. The [Agent Memory Repo](https://github.com/AgentMemoryRepo/agentmemoryrepo)
spec keeps memory as a git repo of linked Markdown files with `MEMORY.md` as
the entry point, and adds dreaming: an agent that runs from time to time,
adds what repeats across sessions, and cleans the memory up.

## Decision

- One memory repo per dex data directory, at `<data dir>/memory/`. dex has one
  user, and the spec keeps memory per person, so all agents and channels share
  it.
- The worker owns the repo. A pi-durable extension shows the rules and
  `MEMORY.md` in the system prompt, read again before each model request. The
  agent edits files with its own tools.
- The worker commits, not the agent: after each submission settles, and once
  when it opens the memory, for a change that a crash left. A model does not
  need to know git, and a failed git command cannot fail a chat.
- A dream is a hidden chat with one message, so it uses the durable delivery
  of messages: it survives a restart, and a retry finds the same submission.
  Control schedules it and records, in the `dreams` table, which messages
  each dream read.
- No approval step and no UI. Git history is the audit trail and the undo.
- Local only: no remote, no pull, no push.

## Consequences

- A memory change applies from the next model request in every chat.
- A dream costs model tokens at most once in 24 h, and only after new chats.
  A failed dream reads the same messages again 24 h later.
- Two chats that edit the same file at the same time rely on the edit tool,
  which fails on stale text. The `write` tool can still overwrite a file; the
  rules allow it only for new files.
- Dreams read the user's messages and the final answers from
  `control.sqlite`, not the tool calls. A dream cannot check a claim against
  tool output.
- To share memory between computers, add pull and push to the commit step.
