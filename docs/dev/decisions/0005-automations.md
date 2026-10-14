# 0005: Automations: prompts on a schedule

Status: Accepted (2026-10-14)

## Context

The user wants the agent to run a prompt on a schedule, such as a morning
brief. dex runs on a laptop, which sleeps and restarts. A later agent tool will
manage automations too.

## Decision

- An automation is a row in `automations` in `control.sqlite`, with a prompt,
  a schedule (every N minutes, daily, weekly, or once), an IANA time zone, and
  a session mode. The web UI manages them under "Automations".
- Two session modes. `new` starts the chat `automation:<id>:<run id>` for each
  run. `reuse` sends each run to the chat `automation:<id>`, so the agent sees
  the earlier runs. An automation cannot post into a chat of the user, such as
  a Telegram chat.
- The schedule lives in the database. Each automation stores its next time
  (`next_due_at`). One gocron job checks every minute for automations whose
  time has come. dex computes the times in Go, in the automation's time zone:
  gocron keeps no state across a restart, and its cron jobs with a time zone
  can run twice when a DST change repeats an hour.
- A missed time runs once, only when it is at most 1 h old. Older missed times
  are skipped, and the run history shows the newest of them as skipped. A
  change to an automation computes its next time from now.
- A run is skipped when the agent has no model or a chat of the automation
  has a message without an answer. A run is a row in `automation_runs`; a
  started run has a message with the same ID, which gives its status.
- Automation chats take replies in the web UI. The sidebar keeps them in a
  separate, collapsed group.

## Consequences

- A run starts up to one check interval (1 min) after its time.
- A crash between storing a run and storing its message loses that run; the
  history shows it as failed. The next time runs as usual.
- A chat in `reuse` mode grows without limit on disk. pi-durable compacts the
  model context.
- Deleting an automation removes its runs; its chats stay.
