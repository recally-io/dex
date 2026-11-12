# 0018: No built-in coding skill, and supervised processes

Status: Accepted (2026-11-12). Amends [ADR 0015](0015-child-sessions.md).

## Context

dex added an always-on `coding-skill` prompt section. It held a coding method
(review, tests, reports, commit rules) and the usage of the session tools.

The method is a choice of the user. A user who writes a `coding` skill got two
conflicting instructions. The first fix let a user skill replace the section.
That fix needed a file check at each prompt build, and it still made dex own
a method that it cannot enforce.

The usage of the session tools belongs to the tools. A model reads a tool
description at the moment it picks the tool. It reads a long prompt section
less reliably.

The child rules and the skill also said "Do not detach processes or start
daemons". A user's own coding skill can run a worker in a Rex terminal tab.
`rex run` returns at once, the agent polls the tab, and it closes the tab before
the final answer. A child session refused this because of the rule.

## Decision

dex has no built-in coding skill. A `coding` skill at any level is an ordinary
skill. It is listed like the other skills and it can be overridden by the
normal skill levels.

The usage of the session tools is in the descriptions of those tools:

- `session_send` continues the same child with its context, and says that the
  system does not retry a failed turn or its result delivery.
- `session_new` says that children start at once, have no count limit, and run
  at the same time in one directory. It also says how to choose `bot`.
- `session_stop` withdraws queued input and stops the running turn. It does not
  roll back files.
- `session_list` is called before a progress answer.
- `session_read` is called for a result only when the result is cut.

The `work-loop`, `front-desk`, `child-rules`, and `session-rules` sections keep
the rules of a whole loop. A sentence that one tool description owns is not
repeated there.

The child rule about processes is now: do not leave processes running after
the turn ends. A process that the agent starts and supervises in the same turn
is allowed. The agent stops it and closes its tab before the final answer.
Before new work, the agent checks for processes or tabs that an earlier
interrupted turn left. Ordinary CLI commands stay foreground and
non-interactive.

## Consequences

The prompt has no coding method. Review, test, report, and commit rules come
from the user's skills, the project `AGENTS.md`, and the user's messages.

Stored conversations are not affected. A conversation stores only the names of
extensions that dex added or removed for it. The `coding-skill` extension was in
the default selection, and no conversation names it. The SDK ignores a stored
name that is not installed. Old system prompt entries that contain a
`coding-skill` section stay as history, and the next request builds a new prompt.

The SDK still does not track process trees. The supervised-process rule depends
on the agent to clean up, so an interrupted turn can leave a tab behind. The
"check before new work" step is the recovery.
