# 0018: Allow supervised processes and let a user coding skill replace the built-in one

Status: Accepted (2026-11-12). Amends [ADR 0015](0015-child-sessions.md).

## Context

The child rules and the built-in coding skill said "Do not detach processes or
start daemons". A user's own coding skill can run a worker in a Rex terminal
tab. `rex run` returns at once, the agent polls the tab, and it closes the tab
before the final answer. A child session refused this because of the rule.

The built-in coding skill is an always-on prompt section. A user skill with the
same name was only a second, conflicting instruction.

## Decision

The rule is now: do not leave processes running after the turn ends. A process
that the agent starts and supervises in the same turn is allowed. The agent
stops it and closes its tab before the final answer. Before new work, the agent
checks for processes or tabs that an earlier interrupted turn left. Ordinary
CLI commands stay foreground and non-interactive. The child rules and the
built-in skill carry this text.

A valid skill named `coding` at the dex or agent level replaces the built-in
`coding-skill` section. The user skill is listed like any other skill. A
project-level `coding` skill does not replace it, because a repository must not
choose how the agent works.

The section checks the dex and agent skill directories each time the prompt is
built. This is the same time at which the skills section reads files, so an
edit applies at the next model request, without a reload. A skill with a front
matter error does not count, because it is not listed.

## Consequences

The SDK still does not track process trees. The rule depends on the agent to
clean up, so an interrupted turn can leave a tab behind. The "check before new
work" step is the recovery. Without a user `coding` skill, nothing changes
except the rule text.
