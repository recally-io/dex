# CI and releases

## CI checks

[Check](../../.github/workflows/check.yml) runs on each push to `main` and each
pull request. Its jobs run the steps of `mise run check` in parallel: lint and
drift, web, Go lint and tests, and native checks and cross-builds.

Three app jobs partition the `internal/app` tests and run them without the
race detector, because it made them several times slower for little gain.
Four session jobs partition the `internal/session` tests and run them with the race
detector. The Go job uses `check:go without-integration` for all other
packages; local `check:go` still runs every package. Run
`mise run check:go-shard ./internal/session 0 4` to repeat the first session
partition locally, or `mise run check:app` for all app tests. The app and session
jobs save their JSON timings as a seven-day artifact. The jobs share the main
Go cache without saving duplicate archives. Before the save on `main`, the Go
and native jobs delete the cache entries that they did not use, so the cache
does not grow on each run.
The native job builds darwin and linux for amd64 and arm64 without cgo.
When you add a step to `check` in `mise.toml`, add it to the workflow too.

## Releases

Prepare and publish releases from a `release/*` branch. Do not bump the version
or tag a release on `main`.

1. For a new release series, cut a release branch from the reviewed commit that
   starts that series. For example, cut `release/v0.20.0` from the selected
   `main` commit before making release changes. For a patch, use the existing
   branch for that series. If it does not exist, cut it from the last published
   tag. For example, branch from `v0.19.0` for `v0.19.1`.
2. For a patch, start the fix branch from the release branch and target that
   release branch in the pull request. Merge the fix after Check passes. Do not
   merge `main` into a maintenance branch.
3. On the release branch, run
   `mise run release:bump <patch|minor|major|X.Y.Z|X.Y.Z-rc.N>` to update
   [VERSION](../../VERSION) and roll `[Unreleased]` in
   [CHANGELOG.md](../../CHANGELOG.md) into a dated section. A version with a
   hyphen, such as `0.20.0-rc.1`, makes a prerelease. Review the notes and commit
   the version bump separately from the fixes.
4. Push the updated release branch. Start
   [Release](../../.github/workflows/release.yml) with
   `gh workflow run release.yml --ref <release-branch>`. The workflow reads the
   branch's `VERSION`, runs Check, creates the `v<VERSION>` tag on the tested
   commit, and runs [GoReleaser](../../.goreleaser.yaml) on Ubuntu.
5. For fixes that are not yet on `main`, start a separate branch from `main`,
   cherry-pick the fix commits with `git cherry-pick -x`, and open a pull request
   to `main`. Do not cherry-pick the release version bump. If a fix needs a
   changelog note on `main`, put it under `[Unreleased]`.

### Release artifacts

GoReleaser builds `dex` for Linux and macOS, x64 and arm64, and uploads a
draft release with one `dex_<tag>_<os>_<arch>.tar.gz` for each platform,
`checksums.txt`, and notes from [CHANGELOG.md](../../CHANGELOG.md) (or from the
pull requests when no matching changelog section exists). GoReleaser builds the
web UI once and embeds it in each Go binary. Smoke tests start the Linux
(amd64 and arm64) and macOS arm64 binaries on their own platforms. When all
smoke tests pass, the workflow publishes the draft. dex reads its version from
the tag. The workflow also accepts a `v*` tag push. Use the release-branch
procedure above for planned releases and patches.

To try the build on your machine, run
`goreleaser release --snapshot --clean --parallelism 1`. The output goes to
`dist/`.

The macOS binaries are signed ad hoc, not notarized. macOS blocks a binary
from a browser download; run `xattr -d com.apple.quarantine dex` first.
