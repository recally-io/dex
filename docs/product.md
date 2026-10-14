# Product

This document is for anyone who decides what dex does. It states the users,
the problem, the scope, and the non-goals.

## Users and problem

dex is for one person or a small team who wants personal AI agents that they
can reach from chat apps and a browser, and that keep working over long
tasks. Hosted agent platforms keep the data elsewhere and are hard to change.
dex runs on one machine, keeps all data in local files, and stays small enough
to read in an afternoon.

## Scope

- One `dex` binary. The host needs no other runtime.
- Agents with a model, instructions, and a workspace directory.
- Durable conversations: a crash or a restart does not lose a message or a
  started run.
- Entry points: the web UI and Telegram.
- Timers: scheduled messages to an agent.

Status: the web UI, durable messages, and the worker are done. Telegram and
timers are next.

## Non-goals

- More than one host. One machine runs control and one worker.
- Sandboxes. Agent tools run on the host, inside the agent workspace.
- Shared object storage. Agent files are local directories.
- Automatic failover or data replication.
