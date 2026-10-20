# 0011: Run PiG in one Go process

Status: Accepted (2026-10-20).

Supersedes [ADR 0001](0001-embedded-bun-worker.md). Replaces the backend
transport in [ADR 0003](0003-web-ui-stack-and-live-transcript.md), the worker
readiness and maintenance lifecycle in [ADR 0006](0006-service-and-update.md),
and whole-worker reload in [ADR 0010](0010-extension-levels.md). Other product,
service, environment, and channel contracts remain in effect.

## Context

PiG provides the durable execution, model, and MCP libraries in Go. dex can
run those libraries in its existing process. The old backend required an
embedded Bun executable, a request protocol, process supervision, event
routing, and two transcript projections.

This migration targets the final native architecture. The old backend and
storage version do not need a permanent compatibility layer. Existing local
dev history must remain readable and usable as model context. Data conversion
can use a one-time importer. Validation uses copies of real data.

## Decision

Run HTTP, business services, and `internal/engine` in one Go process. The engine
uses a pinned PiG module. It owns one Harness per agent, native storage,
model configuration, tools, MCP connections, and transcript projection.
Remove the backend subprocess, internal JSON protocol, worker build, and
embedded worker artifact. Tool processes and stdio MCP servers remain normal
execution dependencies.

Business services call concrete `engine.Agents` methods. `Ref{AgentID, ID}`
names an execution conversation. Business chat IDs remain separate and store
an `execution_id` reference. Task tools call injected typed business handlers.
The engine does not import the HTTP API package.

### Execution and business storage

PiG owns admission, Inbox order, run and tool state, transcript entries,
cancellation, and native outcomes in each agent's `state.sqlite`.
`control.sqlite` owns settings, input intents, admission and outcome receipts,
task records, and confirmed channel reply parts.

The stores have separate commits. A business receipt is not proof of native
admission. `Submit` uses a stable request ID and returns only after durable
admission. The business service then marks the receipt admitted and waits on
the native handle. If either step fails, reconciliation retries the same
request ID. It can admit an input that never reached PiG or reacquire one
that PiG already admitted. Cancellation of `Submission.Wait` cancels only
the wait.

Task results always enter the native follow-up Inbox, including when the
source is busy. There is no reject-when-busy mode or business retry queue.
Stop withdraws queued results and the transcript shows their withdrawn state.
The saved task report stays readable. A later input does not revive a
withdrawn result.

Startup reconciles ordinary inputs and saved outcomes. It registers started
task conversations for isolation before Harness scheduling resumes. The same
business transaction marks started tasks interrupted and saves their cancellation
intents. A restart before the agent opens does not lose those intents. Started
tasks do not run again as new task executions. Saved
channel replies remain in the outbox until delivery is recorded. Channel
reply delivery remains at least once; a remote success before the local
confirmation write can produce a duplicate reply part.

### One transcript projection

The engine defines typed frontend `Item`, `Event`, `Retry`, and `History`
values with the existing JSON field names. One renderer serves history,
watch snapshots, and incremental changes. The live hub only handles SSE,
heartbeats, and attachment of an initially empty chat.

Native input metadata holds request ID, original content, origin,
attachments, source, and optional task ID and title. Queued input comes from
the native Inbox. Stop atomically records withdrawn input as passive
`dex.withdrawn` entries before active cancellation. Those entries show the
input and an aborted error but add no model context. Task result entries
show a task mark without exposing the report as ordinary user text.
There is no permanent merge of control receipts into transcript history.
A one-time importer can hydrate missing metadata in old dev records.

Each browser has an independent native watch. The initial snapshot and
subscription share a native revision. Cancellation closes only that watch.
A slow reader can buffer at most 256 frontend events. Overflow ends the
watch; the browser reconnects for a new snapshot. SSE keeps the 15-second
heartbeat. There are no watch IDs, process-exit callbacks, or internal RPCs.

### Resource lifetime and environments

The engine owns admission gates and idle inspection. The chat service owns
the two-minute maintenance lease and resumes admission on release or expiry.
A busy acquisition fails and releases the gate. While the lease is active,
web input returns `409` before a business receipt is written. The client can
retry the same request ID after release or expiry. Channel input retains its
business receipt until admission resumes. Process restart clears the lease.
Existing subscriptions can continue during maintenance; new transcript
acquisitions use the admission gate.

Reload validates and replaces only the selected agent's Harness and MCP
connections. Other agents keep their resources. The idle gate prevents
replacement during active execution. Preflight failure keeps the current
resources. Shutdown closes native resources and business storage in order.
Readiness checks the engine lifecycle in the same process. The health response
uses `runtimeReady`.

Tools and stdio MCP servers receive an explicit environment. Home, dex,
and agent `.env` values keep ADR 0010's precedence and write-only editing
rules. A server's own `env` wins. The engine does not change process-wide
environment values or inherit ambient credentials into tools. Memory Git
commands use the explicit base environment and disable repository hooks and
fsmonitor commands. Tools still run as the user without a sandbox.

Model discovery has a 30-second deadline for each provider request. A discovery
failure remains a provider error, so the user can repair that provider in Settings.

## Consequences

The release contains a Go binary and the embedded web UI. The host needs
neither Bun nor Node. Web development and builds can still use those tools.
Go checks exercise the real PiG engine and SQLite directly. Projection tests
cover stream, tool, history, task, queue, and withdrawal cases. HTTP and
channel tests retain the product contracts. A faux-provider check does not
prove real-provider behavior; a real-provider run remains separate.

Business and execution stores keep separate owners inside one process.
Agent opening and Reload currently share one engine lock. A slow MCP connection
can delay other engine calls, including health checks. Use separate opening locks
for each agent if that delay becomes a runtime problem.

There is no cross-store transaction. Stable request IDs and reconciliation
remain necessary. Native durability does not make external side effects
exactly once. A stopped process cannot undo a tool's file writes or remote
actions. Backup still requires stopping dex and copying its complete data
directory. This change adds no automatic binary or data rollback.
