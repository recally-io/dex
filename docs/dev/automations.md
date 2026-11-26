# Automations

This document describes automations, schedules, and their results.

Main and writable Chat conversations have five automation tools:
`automation_list`, `automation_get`, `automation_create`, `automation_update`, and
`automation_run`. Typed engine callbacks call the automation service. The model
cannot select another bot. Business validation checks the calling execution
and input before it exposes automation data or changes. Automation, task, and
dream conversations cannot use these tools.

Updates are partial. An enabled-only update pauses or resumes an automation
without replacing its prompt or project directory. Creation inherits the
calling conversation's project when `cwd` is omitted. A run result includes
its chat ID so the agent can link to the saved transcript. Tool results name
the duplicate-finding flag `repeatedFinding` to distinguish it from execution
counts. The HTTP API keeps its `duplicate` field. The tools do not provide deletion.

Each mutating tool call uses its bot ID and native tool task ID as a stable
operation key. The control database commits its operation receipt with the
automation change or run input. Replay returns the saved result. It does not
repeat the write or advance the schedule. A new mutation requires an active
user input; a saved receipt can still be read after that input settles.

After an upgrade, PiG cannot resume a pending call under a retired tool name.
A committed change stays saved even if PiG did not save the tool result.
Check the saved automation or run before you retry. Update saved prompts that
refer to retired tool names.

An automation runs a prompt on a schedule.
[`automation.Service`](../../internal/automation/automation.go) stores
automations and their runs in `control.sqlite`. One scheduler job calls its
`Check` every minute for all agents. Each row supplies its owning agent ID.
`Check` posts the prompt of each automation whose
`next_due_at` has come, as a message to its chat (`automation:<id>`, or
`automation:<id>:<run id>` for a new chat each run), or to a saved existing
session, and moves `next_due_at` past now. The message uses normal durable
delivery.

In `session` mode, the Automation stores the ID of a writable, unarchived session
of its own bot. Sessions with a restricted execution scope do not qualify.
Deleting the session clears the reference. Both a save and the input writer
validate the target. A missing or ineligible target skips the run with a reason.
An Automation does not create or fall back to another session. Its prompt queues as
a follow-up if the session is busy. Only a pending input of the same Automation
counts as overlap. Stop and Clear work on the session as usual.

The Automation API and tools select a Runner with `runnerId`. An empty value
follows the bot. `local` or an SSH Machine ID from `runner_list` pins the Runner.
The owner cannot select
a Sandbox or Outbound Machine as an explicit override. `automation_create`
uses the calling session's Runner and cwd by default. A run never falls back to
another machine. Before a run, outside any transaction, `Preflight` resolves the
effective environment and checks it: a removed target skips the run, and an SSH
target is opened with a 15-second timeout, so a host that does not answer skips
the run. E2B and runner targets are not opened, because that starts the work.
The input transaction resolves the environment again under the writer lock and
stores the run or the skip only when it is still the checked one and the automation
row did not change, also for a manual run; otherwise a manual run fails with 409
and can run again, and a scheduled run is checked again next minute. `automation_run` uses the same preflight
before its tool transaction. The skip reason names the environment. A change of
the environment without a new cwd clears the cwd. A change of the bot's
environment clears the cwd of the automations that follow the bot.
In `session` mode the Automation has no environment or cwd of its own. Preflight
checks the session's effective environment, including whether a Runner is online.
The writer records the session's environment and cwd on the run, but does not
change these settings on the session.

Each run stores its effective environment and its cwd
(`automation_runs.runner_id`, `cwd`). Runs from before this column have an
empty binding, because where they ran is not known. A reused chat
(`automation:<id>`) continues only when its last run has the same effective
environment, so an automation that follows the bot also gets a new chat after the
bot's environment changes, and a chat whose last run has an empty binding starts
again. The old chat gets the key `automation:<id>:<run id>` and stays readable.
A chat that continues a result pins `local` or the SSH environment of that run
and uses its cwd. A run in an E2B or runner environment, or with an empty
binding, cannot be continued that way, because a chat cannot pin that
environment.

The `automation_notification_targets` table stores authorized recipients. The
`automation_tool_operations` table stores committed tool results for replay.
A user can select a primary notification target for each agent. Each automation can
turn notifications off, follow the primary target, or select another authorized
recipient of that agent. Existing automations keep notifications off. The API keeps
an omitted notification field on update. New automations created in the dialog or
by the automation tool follow the primary target. With no primary target, the run
has no channel delivery.

A settled automation with a resolved authorized target saves one delivery with the
result in the same transaction as settlement. It freezes the target and message at that
time. A skipped failure can queue an alert without starting a model turn. This
includes a skip for a missing or ineligible bound session. Channel managers render and save message
parts before sending. They retry pending parts after a failure or restart.
Delivery is at least once: a platform can accept a part before the local receipt
is stored. A permanent rejection is recorded separately from a delivery.
Changing an automation route or the primary target affects future settlements, not
already queued deliveries. Removing channel authorization rejects its pending
deliveries. A target's deletion also clears automations that selected that target.
Channel changes stop and join the old runtime before they change authorization.
They cannot retract a message already accepted by a platform. WeChat delivery
also needs a current owner context token and can wait for one.

Run records and durable message intents commit in one control transaction.
Before it stores scheduled input, that transaction takes the SQLite writer and
checks that the automation still matches the scheduler's snapshot. A saved edit,
pause, or deletion invalidates the old snapshot. The run ID is the input request ID. A crash before commit leaves neither
record. A crash after commit leaves an input that normal chat reconciliation
can admit. If the schedule cursor did not advance, the unique automation/due
key prevents another input for that scheduled time.

An automation can store a project `cwd`. The API uses the Chat path validator.
In `new` and `reuse` mode, each run sets or clears its chat's cwd before it stores the input. Project
skills therefore follow the run directory, including when a chat is reused.

Only an Automation input can write a report with `automation_report`, including a run
bound to an existing session. The tool is visible in unscoped sessions, but
checks the current input's source receipt before it writes a result. Bound
Automation inputs include reporting instructions. A failed Automation run in a child
session does not send an automatic child error to the parent. The tool stores
an explicit `changed`, `unchanged`, or `failed` outcome, a summary, and an optional change
key. The report is provisional while the run is active. Submission settlement
and the final run outcome commit together. Missing reports and execution errors
produce `failed`; answer text cannot imply `unchanged`. Old terminal runs
without reports keep an unknown outcome. Old started runs without an input
migrate to `failed`. Skipped runs have no outcome.

A completed report with the same nonempty change key as an earlier
completed report has `duplicate: true`. Both run records remain. Deduplication
records repeated findings; it does not prevent or undo actions. The runs API
exposes these fields in the automation's run history.

Failed runs and runs skipped due to no model, a missing or unavailable
execution environment, or a missed schedule can queue an alert in the channel
Outbox with kind `automation`. A skip due to a busy Automation does not queue one. Dex compares the
failure reason with the previous finished run of that Automation. Only an exact
match with a previous failure suppresses the alert. This comparison does not
change report deduplication. A skip has no input; its Outbox entry uses an
existing conversation of the same bot only as its owner. It does not start a
turn in that conversation. If no target resolves, the run list shows
`not delivered: no notification target`.

The owner can inspect recent delivery records in the Outbox panel of a
read-only Automation transcript. A pre-start skip has no transcript. Its record
stays in the Outbox of the conversation that owns the row. Automation records do
not add notification entries to the Main timeline.

`automation_runs.read_at` and `dismissed_at` hold result state. `GET /api/automations/results`
uses one query across all Bots. It joins runs, Automations, Bots, and inputs.
Settled failed runs and non-duplicate changed runs qualify.
The query excludes dismissed results unless `includeDismissed=true`.
Read and dismiss endpoints keep the first timestamp and return 404 for runs
that do not qualify. Repeated writes are safe.

The web uses the shared list for result details and Automation history. It refreshes
the list every 10 seconds while either page is open. Read and dismiss writes
invalidate the result list and Board attention. Dismissed details remain readable by URL.
Continue in a session creates a Chat, sets its Runner and project, and sends a message. Its first message quotes the automation name, outcome, summary, and
run transcript reference. A retry in the same detail view keeps the Chat and
message request IDs.
