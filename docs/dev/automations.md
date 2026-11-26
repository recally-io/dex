# Automations

This document describes automations, schedules, and their results.

Main and writable top-level Web sessions have five automation tools:
`automation_list`, `automation_get`, `automation_create`, `automation_update`, and
`automation_run`. Typed engine callbacks call the automation service. The model
cannot select another bot. Business validation checks the calling execution
and input before it exposes automation data or changes. These tools require a
user turn in Main or a top-level Web session.

Updates are partial. An enabled-only update pauses or resumes an automation
without replacing its prompt or project directory. Creation inherits the
calling session's project when `cwd` is omitted. A run result includes
its session ID so the agent can link to the saved transcript. Tool results name
the duplicate-finding flag `repeatedFinding` to distinguish it from execution
counts. The HTTP API keeps its `duplicate` field. The tools do not provide deletion.

Each mutating tool call uses its bot ID and native tool task ID as a stable
operation key. The control database commits its operation receipt with the
automation change or run input. Replay returns the saved result. It does not
repeat the write or advance the schedule. A new mutation requires an active
user input; a saved receipt can still be read after that input settles.

An automation runs a prompt on a schedule.
[`automation.Service`](../../internal/automation/automation.go) stores
automations and their runs in `control.sqlite`. One scheduler job calls its
`Check` every minute for all agents. Each row supplies its owning agent ID.
`Check` posts the prompt of each automation whose
`next_due_at` has come, as a message to its session (`automation:<id>`, or
`automation:<id>:<run id>` for a new session each run), or to a saved existing
session, and moves `next_due_at` past now. The message uses normal durable
delivery.

In `session` mode, the Automation stores the ID of a writable, unarchived session
of its own Bot, including a child Session. Deleting the Session clears the reference. Both a save and the input writer
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
effective Runner. A removed Runner skips the run. Preflight opens an SSH
Runner with a 15-second timeout. It does not open a Sandbox or an Outbound
Machine. In `session` mode, the Automation uses the target Session's Runner
and project without changing them. Preflight checks if its Runner is online.
The input transaction checks the Runner and Automation again under the writer
lock. If they changed after preflight, a manual run returns `409` and a
scheduled run is checked again the next minute. A change of Runner without a
new `cwd` clears the project. A Bot's Runner change also clears the project
for Automations that follow it.

Each run stores its effective Runner and project (`automation_runs.runner_id`
and `cwd`). A reused Session (`automation:<id>`) continues only if the last run
has the same recorded Runner. Otherwise Dex gives the old Session a
run-specific key (`automation:<id>:<run id>`) and starts a new Session. The old
transcript stays readable.

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
record. A crash after commit leaves an input that normal session reconciliation
can admit. If the schedule cursor did not advance, the unique automation/due
key prevents another input for that scheduled time.

An automation can store a project `cwd`. The API uses the Session path validator.
In `new` and `reuse` mode, each run sets or clears its session's cwd before it stores the input. Project
skills therefore follow the run directory, including when a session is reused.

Only an Automation input can write a report with `automation_report`, including a run
bound to an existing Session. The tool checks the current input's source receipt before it writes a result. Bound
Automation inputs include reporting instructions. A failed Automation run in a child
session does not send an automatic child error to the parent. The tool stores
an explicit `changed`, `unchanged`, or `failed` outcome, a summary, and an optional change
key. The report is provisional while the run is active. Submission settlement
and the final run outcome commit together. Missing reports and execution errors
produce `failed`. Answer text cannot imply `unchanged`. Skipped runs have no
outcome.

A completed report with the same nonempty change key as an earlier
completed report has `duplicate: true`. Both run records remain. Deduplication
records repeated findings; it does not prevent or undo actions. The runs API
exposes these fields in the automation's run history.

Failed runs and runs skipped due to no model, a missing or unavailable
Runner, or a missed schedule can queue an alert in the channel
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
`POST /api/automations/results/{runId}/read` and `/dismiss` keep the first
timestamp and return `404` for runs that do not qualify. Repeated writes are safe.

The Web UI uses the shared list for Automation history and result detail
(`/board/results/$runId`). Board groups unhandled results under **Updates**. It refreshes
the list every 10 seconds while either page is open. Read and dismiss writes
invalidate the result list and Board attention. Dismissed details remain readable by URL.
Continue in a session pins the run's available Local or Machine Runner and
sets its project. A Sandbox, unavailable Runner, or missing Runner record
prevents creation. The first message quotes the Automation name, outcome,
summary, and run transcript reference. A retry in the same detail view keeps the Session and
message request IDs.
