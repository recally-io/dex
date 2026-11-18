# Changelog

All notable changes to `dex` will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [v0.17.0] - 2026-11-18

### Added

- **ExecEnv**: Decouple control plane from execution environment, adding `pi-env` and E2B sandbox lifecycle support (#168, #169, #171, #173).
- **Settings**: Add default bot configuration under `/settings` (#175).

### Changed

- **Web**: Move settings pages under `/settings` (#175).
- **Dev**: Run `dev:server` on port `18787` with `~/.dex-dev` (#176).

### Removed

- **Tests**: Replace legacy browser test scripts with `e2e` (#178).
- **Treewide**: Remove unused code, abstractions, and redundant tests (#177).

## [v0.16.1] - 2026-11-17

### Changed

- **Telegram**: Use Rich Messages for all new text delivery (#165).

### Removed

- **Telegram**: Remove legacy text and delivery shims (#166).

[Unreleased]: https://github.com/recally-io/dex/compare/v0.17.0...HEAD
[v0.17.0]: https://github.com/recally-io/dex/compare/v0.16.1...v0.17.0
[v0.16.1]: https://github.com/recally-io/dex/compare/v0.16.0...v0.16.1
