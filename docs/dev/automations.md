# Automations

This document describes Routines, schedules, and their results.

Main and writable Chat conversations have five routine tools:
`list_routines`, `get_routine`, `create_routine`, `update_routine`, and
`run_routine`. Typed engine callbacks call the automation service. The model
cannot select another bot. Business validation checks the calling execution
and input before it exposes routine data or changes. Automation, task, and
dream conversations cannot use these tools.

Updates are partial. An enabled-only update pauses or resumes a routine
without replacing its prompt or project directory. Creation inherits the
calling conversation's project when `cwd` is omitted. A run result includes
its chat ID so the agent can link to the saved transcript. Tool results name
the duplicate-finding flag `repeatedFinding` to distinguish it from execution
counts. The HTTP API keeps its `duplicate` field. The tools do not provide deletion.

Each mutating tool call uses its bot ID and native tool task ID as a stable
operation key. The control database commits its operation receipt with the
routine change or run input. Replay returns the saved result. It does not
repeat the write or advance the schedule. A new mutation requires an active
user input; a saved receipt can still be read after that input settles.

An automation runs a prompt on a schedule.
[`automation.Service`](../../internal/automation/automation.go) stores
automations and their runs in `control.sqlite`. One scheduler job calls its
`Check` every minute for all agents. Each row supplies its owning agent ID.
`Check` posts the prompt of each automation whose
`next_due_at` has come, as a message to its chat (`automation:<id>`, or
`automation:<id>:<run id>` for a new chat each run), and moves `next_due_at`
past now. The message uses the normal durable delivery.

A routine selects its execution environment with `environmentId`: empty follows
the bot, and `local` or an SSH environment pins the routine. E2B and runner
environments are refused, as for sessions. `create_routine` takes the
environment and the cwd of the chat that calls it. A run never falls back to
another machine. Before a run, outside any transaction, `Preflight` resolves the
effective environment and checks it: a removed target skips the run, and an SSH
target is opened with a 15-second timeout, so a host that does not answer skips
the run. E2B and runner targets are not opened, because that starts the work.
The input transaction resolves the environment again under the writer lock and
stores the run or the skip only when it is still the checked one and the routine
row did not change, also for a manual run; otherwise a manual run fails with 409
and can run again, and a scheduled run is checked again next minute. `run_routine` uses the same preflight
before its tool transaction. The skip reason names the environment. A change of
the environment without a new cwd clears the cwd. A change of the bot's
environment clears the cwd of the routines that follow the bot.

Each run stores its effective environment and its cwd
(`automation_runs.environment_id`, `cwd`). Runs from before this column have an
empty binding, because where they ran is not known. A reused chat
(`automation:<id>`) continues only when its last run has the same effective
environment, so a routine that follows the bot also gets a new chat after the
bot's environment changes, and a chat whose last run has an empty binding starts
again. The old chat gets the key `automation:<id>:<run id>` and stays readable.
A chat that continues a result pins `local` or the SSH environment of that run
and uses its cwd. A run in an E2B or runner environment, or with an empty
binding, cannot be continued that way, because a chat cannot pin that
environment.

A user can select a primary notification target for each agent. Each routine can
turn notifications off, follow the primary target, or select another authorized
recipient of that agent. Existing routines keep notifications off. The API keeps
an omitted notification field on update. New routines created in the dialog or
by the routine tool follow the primary target. With no primary target, the run
has no channel delivery.

A settled routine with a resolved authorized target saves one delivery with the
result in the same transaction as settlement. It freezes the target and message at that
time. A skipped run has no delivery. Channel managers render and save message
parts before sending. They retry pending parts after a failure or restart.
Delivery is at least once: a platform can accept a part before the local receipt
is stored. A permanent rejection is recorded separately from a delivery.
Changing a routine route or the primary target affects future settlements, not
already queued deliveries. Removing channel authorization rejects its pending
deliveries. A target's deletion also clears routines that selected that target.
Channel changes stop and join the old runtime before they change authorization.
They cannot retract a message already accepted by a platform. WeChat delivery
also needs a current owner context token and can wait for one.

Run records and durable message intents commit in one control transaction.
Before it stores scheduled input, that transaction takes the SQLite writer and
checks that the routine still matches the scheduler's snapshot. A saved edit,
pause, or deletion invalidates the old snapshot. The run ID is the input request ID. A crash before commit leaves neither
record. A crash after commit leaves an input that normal chat reconciliation
can admit. If the schedule cursor did not advance, the unique automation/due
key prevents another input for that scheduled time.

An automation can store a project `cwd`. The API uses the Chat path validator.
Each run sets or clears its chat's cwd before it stores the input. Project
skills therefore follow the run directory, including when a chat is reused.

Only automation conversations have `report_check`. The tool stores an explicit
`changed`, `unchanged`, or `failed` outcome, a summary, and an optional change
key. The report is provisional while the run is active. Submission settlement
and the final run outcome commit together. Missing reports and execution errors
produce `failed`; answer text cannot imply `unchanged`. Old terminal runs
without reports keep an unknown outcome. Old started runs without an input
migrate to `failed`. Skipped runs have no outcome.

A completed report with the same nonempty change key as an earlier
completed report has `duplicate: true`. Both run records remain. Deduplication
records repeated findings; it does not prevent or undo actions. The runs API
exposes these fields in routine check history. The web calls automations
Routines; API paths and code names remain `automation`.

Failed runs and runs skipped due to no model, a missing or unavailable
execution environment, or a missed schedule can queue an alert in the channel
Outbox. A skip due to a busy Routine does not queue one. Dex compares the
failure reason with the previous finished run of that Routine. Only an exact
match with a previous failure suppresses the alert. This comparison does not
change report deduplication. A skip has no input; its Outbox entry uses an
existing conversation of the same bot only as its owner. It does not start a
turn in that conversation. If no target resolves, the run list shows
`not delivered: no notification target`.

`automation_runs.read_at` and `dismissed_at` hold Inbox state. A separate table
would add a one-to-one record with no independent lifecycle. `GET /api/inbox`
uses one query across all agents. It joins runs, routines, agents, and inputs.
Only settled `changed` and `failed` runs without a duplicate mark qualify.
The query excludes dismissed results unless `includeDismissed=true`.
Read and dismiss endpoints keep the first timestamp and return 404 for runs
that do not qualify. Repeated writes are safe.

The web uses the shared list for result details and history links. It refreshes
the list every 10 seconds while an Inbox page is open. Read and dismiss writes
invalidate the list and agent activity. Dismissed details remain readable by URL.
Continue in chat uses the existing create Chat, project, and message APIs in
that order. Its first message quotes the routine name, outcome, summary, and
run transcript reference. A retry in the same detail view keeps the Chat and
message request IDs.
