# 0022: Execution Environments

Date: 2026-11-17
Status: Accepted

## Context

Agents in dex execute file and shell tools (`read`, `write`, `edit`, `bash`)
in a workspace directory. Running every bot directly on the dex host shares
local files and host credentials across bots.

Users want per-bot isolation over SSH (`pi-env`) or in cloud sandboxes (`e2b`)
that persist across turns and pause automatically when idle, while keeping
`control.sqlite`, channel tokens, uploads, artifacts, and shared memory
(`<data dir>/memory/`) on the control host.

## Decision

### Unified execution boundary (`internal/execenv`)

- `Provider` opens an agent `Handle` (`Workspace`, `Home`, `Env`, `Close`).
- `Manager` resolves `agents.environment_id` from `control.sqlite` (empty
  selects built-in `local`) and dispatches to `local`, `pi-env`, `e2b`, or a
  remote machine ([ADR 0025](0025-remote-machine-enrollment.md)).
- Changing an agent's environment or updating an environment row changes the
  agent stamp so idle sessions reload automatically.

### Environment kinds

1. **`local`**: Uses `NodeExecutionEnv` on `<data dir>/agents/<id>/workspace/`
   with the captured host tool environment.
2. **`pi-env`**: Connects over SSH using TOFU (`accept-new` on first connect,
   strict mismatch refusal afterward) to a remote `pi-env` daemon.
3. **`e2b`**: Calls E2B's control-plane REST API and sandbox `envd`
   HTTP/Connect endpoints using a write-only API key:
   - Sandboxes start with `autoPause: true`, `autoResume: {enabled: true}`, and
     a configurable `timeoutSeconds` (default `300s`). E2B pauses idle sandboxes
     server-side when the timeout expires; `POST /v2/sandboxes/{id}/connect`
     resumes paused sandboxes or renews running ones on demand, and `404`
     provisions a replacement sandbox transparently.
   - `agents.environment_sandbox_id` persists the active sandbox ID across dex
     restarts and clears atomically when `environment_id` changes.
   - `POST /api/agents/{agentId}/environment/reset` kills the sandbox of an
     idle bot and clears `environment_sandbox_id`.

### Control vs execution boundary

- **Memory (`memory_*`)**: `memory_search`, `memory_read`, `memory_write`, and
  `memory_edit` run on `<data dir>/memory/` on the control host and block
  `.git`, symlinks, and other bots' `agents/<otherID>/` directories.
- **Uploads and artifacts**: Incoming uploads stage from control storage into
  the remote workspace on submission; `deliver_file` reads regular workspace
  files back into host artifact storage.
- **Secrets**: Remote `pi-env` and `e2b` shells inherit the remote OS
  environment plus explicit `.env` and 1Password values only; control-host
  process and login secrets are never forwarded.

## Consequences

- Each bot selects `local`, `pi-env`, or `e2b` independently without changing
  conversations, channels, or memory.
- Relying on E2B's native `autoPause` and `/connect` avoids background client
  state machines and keeps sandbox state in `control.sqlite`.
