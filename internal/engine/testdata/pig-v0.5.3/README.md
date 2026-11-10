# PiG v0.5.3 upgrade fixture

`state.sql` is a SQLite dump from dex commit `4a93b06`, which pins PiG
v0.5.3. It contains synthetic data from the real engine and faux providers.
It contains no production data or credentials.

The fixture has three conversations for agent `upgrade`:

- Conversation `2`: one echo answer, a context reset, another echo answer,
  and a bash tool call that prints `legacy-tool`.
- Conversation `29`: a completed task with root request `legacy-task-input`
  and a sealed task binding.
- Conversation `41`: an unfinished background input with a running bash tool.
  The tool creates a marker and waits. Recovery must isolate this conversation
  before scheduling resumes, so the tool cannot create the marker again.

The store includes `pi.live` version 1 documents, `pi.generation` version 1
records and `pi.tool` version 2 records. The fixture checks stored history,
input replay, reset boundaries, task permissions and recovery isolation of
an older running tool.

## Regeneration

Use an isolated checkout of dex commit `4a93b06` with its original module
files. Add `generate_test.go` to `internal/engine` through a Go overlay.
Set `DEX_PIG_FIXTURE_DIR` to a temporary output directory, then run only
`TestGeneratePiG053UpgradeFixture`. The generator uses SQLite `VACUUM INTO`
to capture a consistent store while the tool is running. It then aborts the
tool and closes the engine. The fixture contains the state before cleanup.

Dump that SQLite file with Python's standard-library `sqlite3.Connection.iterdump`
and keep the generated SQL in `state.sql`. Check the conversation IDs in the
generator output. If they change, update this document and the upgrade test.
Do not generate this fixture with the current PiG dependency.

`TestUserUpgradesPiG053StoreWithoutLosingHistoryOrTaskBoundaries` loads the dump
into a temporary data directory and opens it with the current engine. It runs
as part of `mise run check:go`.
