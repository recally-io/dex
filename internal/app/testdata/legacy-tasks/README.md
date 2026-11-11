# Legacy task fixture

`control.sql` and `state.sql` are SQLite dumps from dex commit `096a9c7`. That
commit is the last one with background tasks. The data is synthetic. It comes
from the real engine and the faux provider. It has no production data or
credentials.

The fixture has one agent `default` and these conversations:

- Conversation `1`: the Chat that started the tasks.
- Conversation `2`: task `Ended`. It finished and nobody continued it. Its
  native conversation still has the `task-report` extension edit.
- Conversation `3`: task `Continued`. It finished, and the user wrote one
  message to it.
- Conversation `4`: task `Running`. A bash tool waits at a gate. Its input is
  still pending, so recovery must interrupt it.

`control.sql` has the `task:<id>:result` rows of the two finished tasks.

## Regeneration

Use an isolated checkout of dex commit `096a9c7`. Add `generate_test.go` to
`internal/app` with a Go overlay. Run `mise run build:web` first. Set
`DEX_LEGACY_FIXTURE_DIR` to an empty directory and run only
`TestGenerateLegacyTaskFixture`. The test writes `control.sqlite` and
`agents/default/state.sqlite` with `VACUUM INTO`. Delete the rows of the
`access_token` table, then dump each file with `sqlite3 <file> .dump`. Check
the conversation numbers above.

`TestUserOpensLegacyTaskData` loads the dumps into a temporary data directory
and starts the current app on them.
