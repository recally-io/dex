# CI and releases

## CI checks

[Check](../../.github/workflows/check.yml) runs on each push to `main` and each
pull request. Its jobs run the steps of `mise run check` in parallel: lint and
drift, web, Go lint and tests, and native checks and cross-builds.

Three app jobs partition the `internal/app` tests and run them without the
race detector, because it made them several times slower for little gain.
An engine job runs `internal/engine` with the race detector, and two session
jobs partition `internal/session` with the race detector; neither embeds the
web UI, so they skip the frontend install and build. The Go job uses
`check:go without-integration` for lint, builds, `internal/db` (without
`-race`, since its migration tests run `modernc.org/sqlite` without package
goroutines and warm the shared fresh-database test template), and all other
packages with `-race`; local `check:go` still runs every package. Run
`mise run check:go-shard ./internal/session 0 2` to repeat the first session
partition locally, `mise run check:go-shard ./internal/engine 0 1` for engine
tests, or `mise run check:app` for all app tests. The app, engine, and session
jobs save their JSON timings as a seven-day artifact. The jobs share the main
Go cache without saving duplicate archives. Before the save on `main`, the Go
and native jobs delete the cache entries that they did not use, so the cache
does not grow on each run.
The native job builds darwin and linux for amd64 and arm64 without cgo.
When you add a step to `check` in `mise.toml`, add it to the workflow too.

The [Apple client workflow](../../.github/workflows/apple-client.yml) tests the separate
client module on macOS when client inputs change. It builds development apps for
Apple silicon and iOS Simulator. It does not use signing credentials or publish apps.
See the [Apple client guide](../../apps/native/README.md) for device and distribution builds.

## Releases

Prepare and tag releases on a `release/*` branch. On `main`, `VERSION` tracks the newest stable release prepared for publication. Keep candidate versions on their release branches.

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
4. For a stable release, open a PR to `main` before publication. Copy the final dated changelog section and its compare link. Remove only the shipped entries from `[Unreleased]`. Update `VERSION` and the `[Unreleased]` compare link for the newest stable line. For an older maintenance release, keep the newer version and compare baseline on `main`. Do not replace the whole changelog or copy candidate bumps. Merge this PR after Check passes.
5. Push the updated release branch. Start
   [Release](../../.github/workflows/release.yml) with
   `gh workflow run release.yml --ref <release-branch>`. The workflow reads the
   branch's `VERSION` and requires a nonempty changelog section for the exact tag. For a stable tag, it also checks `origin/main` for matching notes and a stable version at least as new. It then runs Check and packages the Apple silicon and Android ARM64 apps.
   Once all three jobs pass, it creates the `v<VERSION>` tag on the tested commit
   and runs [GoReleaser](../../.goreleaser.yaml) on Ubuntu.
6. For fixes that are not yet on `main`, start a separate branch from `main`,
   cherry-pick the fix commits with `git cherry-pick -x`, and open a pull request
   to `main`. Reconcile the notes in step 4 so that these fixes do not appear twice.

### Release artifacts

GoReleaser builds `dex` for Linux and macOS, x64 and arm64, and uploads a
draft release with `dex_<tag>_<os>_<arch>.tar.gz` and `dex-agent_<tag>_<os>_<arch>.tar.gz` for each platform, `checksums.txt`, and the matching release notes from [CHANGELOG.md](../../CHANGELOG.md). GoReleaser builds the
web UI once and embeds it in each Go binary. Smoke tests start the Linux
(amd64 and arm64) and macOS arm64 binaries on their own platforms. When all
smoke tests pass, the workflow publishes the draft. dex reads its version from
the tag. The workflow also accepts a `v*` tag push. Use the release-branch
procedure above for planned releases and patches.

Starting with the v0.21.0 series, both candidate and stable releases include `Dex-<tag>-macOS-arm64.zip`. The published v0.20.0 release predates the Mac app. GoReleaser adds the Mac ZIP to the draft and includes its SHA-256 in `checksums.txt`. The Mac smoke job downloads that ZIP, verifies its checksum, extracts `Dex.app`, and checks the code signature, architecture and version before publication. The app uses an ad hoc signature; the release does not need Apple credentials. This workflow does not publish iPhone or simulator builds.

Starting with the v0.21.0 series, each release also includes `Dex-<tag>-Android-arm64.apk` for Android 9 and later. GoReleaser includes the APK in `checksums.txt`. Before it creates the tag, the workflow checks the APK signature, certificate digest, ABI, version name, and build number. Before publication it downloads the APK from the draft and verifies its checksum and signature again.

Keep the Android distribution key and a recovery copy outside Git. Updates must use the same key. Configure repository secrets `DEX_ANDROID_KEYSTORE_B64` (base64 of a single-key RSA PKCS#12 file) and `DEX_ANDROID_KEYSTORE_PASSWORD` (store and key password). Set the repository variable `DEX_ANDROID_CERT_SHA256` to the signing certificate's lowercase SHA-256 digest without separators. The release task stops if any value is missing or the certificate does not match. It never uses the debug key for release. The release workflow run number supplies the Android `versionCode`; do not reset it across upgrades.

On an Apple silicon Mac, run `mise run release:mac-app` to build the same ZIP without creating a tag or publishing a release. The task reads `VERSION` by default, or accepts a tag such as `v1.2.3`. Find the ZIP in `.local/release/<tag>/`. The filename keeps the full tag. The app uses the numeric part for `CFBundleShortVersionString` and the workflow run number (locally, `1`) for `CFBundleVersion`.

To try the server build on your machine, run
`goreleaser release --snapshot --clean --parallelism 1`. The output goes to
`dist/`.

The macOS binaries are signed ad hoc, not notarized. macOS blocks a binary
from a browser download; run `xattr -d com.apple.quarantine dex` first.
