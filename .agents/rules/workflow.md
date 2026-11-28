# Workflow

Read this for every task.

## Commands

- Run commands through mise: `mise tasks` lists them, `mise run <task>` runs
  one. Do not copy a task's commands into a shell or a document.
- Put a new task with more than one command in `.mise/tasks/<group>/<name>`,
  with a `#MISE description=` line. Keep one-line tasks and the aggregate tasks
  (`install`, `format`, `check`, `build`) in `mise.toml`.
- After a clone, run `mise install`, `mise run install`,
  `mise run skills:install`, and `mise run hooks:install`.

## Commits

- Format the subject as `<scope>: <description>`. The scope is the area that
  changed: `control`, `web`, `db`, `docs`, `ci`, `treewide`. Do not
  use Conventional Commit types such as `feat:` or `fix:`. Emoji are allowed.
- Write the subject in the imperative, under about 72 characters. Use the body
  for the reason when the diff does not show it.
- Make each commit one logical change. Commit formatter output separately from
  behavior changes.
- The pre-push hook runs `mise run check:quick` and blocks a push on failure.
  It runs static checks, Go lint and build, and checks selected by changed
  paths. It tests changed Go packages without `-race` and skips `internal/app`.
  On macOS it runs Apple client checks when client inputs change.
- CI runs the full `mise run check`, including the `internal/app` tests
  without the race detector.
  Mark a PR ready or merge it only when CI passes.
- Do not amend or force-push unless the user asks. Use
  `git push --force-with-lease`, never `--force`.
- Do not add `Signed-off-by`. Never commit secrets, `.env`, or `.local/`.
- Before you commit, read the staged diff. After you commit, read
  `git show --stat HEAD`.

## Issues and pull requests

Creating or editing an issue, a PR, a comment, a release, or a label publishes
content. Do it only when the user asks for it in the current task. Use `gh`
with `--body-file`. Use the templates in `.github/`.

- Open an issue before work that changes external behavior, spans several
  areas, or needs a decision. Write acceptance criteria as user scenarios.
- One PR has one purpose. Link the issue (`Closes #123`, `Refs #123`, or
  `No issue: <reason>`). List the checks you ran and their results, and the
  checks you did not run.
- Merge only when the user authorizes it, with
  `gh pr merge --match-head-commit <reviewed sha>`.
- Releases: follow [releasing](../../docs/dev/releasing.md) (`mise run
  release:bump` updates `VERSION` and `CHANGELOG.md`).
