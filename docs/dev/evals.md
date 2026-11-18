# Real-provider agent evals

Run the nineteen versioned tasks through the real HTTP API:

```sh
mise run eval:agents --providers-from /path/to/control.sqlite --model provider/model-id
```

Use `--model provider/model-id` to select a configured model at run time.
The saved baseline uses `deepseek-flash`. Reports store only the model short
name; provider names and routing prefixes stay out of committed summaries. The source
SQLite file is opened read-only. Only provider rows are copied. Each task
starts dex with a temporary data directory, home and fixture Git repository.
The command never uses the source directory as its data directory. It does not
load home skills, MCP configuration or channel settings.

Each task runs twice by default. Use `--n 3` for three repetitions,
`--task 01-factual` for one task, or `--timeout 5m` to set the task deadline.
Tasks and short review rubrics live in `evals/tasks`. Deterministic checks do
not replace human assessment of the rubric. Coding tasks grade fixture tests
and files. They record delegation as a metric. Tasks 09 and 10 check explicit
parallel-work and review requests. Tasks 11 and 12 check execution judgment
without delegation instructions. Task 05 limits parent tool output to 8192
UTF-8 bytes so large output in the parent context fails the check.
Task 13 checks routine management. It reads saved routines and runs through
HTTP, waits for routine execution to settle, and checks the final schedule,
paused state, inherited project, result, and transcript reference. The task
uses one local file and does not call an external service from the routine.
Tasks `16-session-steer`, `17-session-follow-up`, `18-session-fork`, and
`19-session-compact` check session operations and replay through the HTTP API.
They do not cover channel-origin steer, crash windows, or cross-bot operations.

Results go to `.local/evals/<timestamp>/results.json` and `report.md`.
The JSON contains checks, transcripts from the parent and child conversations,
wall time, model turns, tool calls, spawn counts and reported tokens. Model
turns count distinct assistant entries, including entries with tool calls only.
The HTTP transcript reports tokens only for turns with text, so token totals
can be lower than provider billing totals. The API does not expose cost.
The command makes real, billable requests. The default run starts 38 tasks;
delegation and review add requests. Use a single-task run to check credentials
before a full run. Provider prices, task retries and model behavior set the cost.

The Markdown report compares each check with `evals/baseline.json` and marks
pass-to-fail and fail-to-pass changes. Use `--baseline <file>` for another
summary. To save a summary without transcripts:

```sh
mise run eval:agents --providers-from /path/to/control.sqlite --model provider/model-id --save-summary evals/baseline.json
```

Keep transcripts under `.local/`. Do not commit them. Summary files contain
fixture check results and metrics. API keys are removed before JSON is saved.
The eval program uses the `agenteval` build tag and is outside `check`.

Run `mise run eval:check` to lint the tagged runner and test provider-copy
isolation, artifact redaction, and routine result checks without provider requests.
