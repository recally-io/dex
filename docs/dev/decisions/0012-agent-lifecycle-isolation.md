# 0012: Isolate agent resource and delivery work

Status: Accepted (2026-10-20).

Supersedes only the global opening-lock ceiling in
[ADR 0011](0011-native-pig-runtime.md). Its storage, recovery, environment,
transcript, and native admission decisions remain in effect.

## Context

A slow MCP connection held the engine's global lock during agent open or
Reload. Reload also held the chat service's global delivery lock. Other
agents and health checks could wait for that connection. Narrowing only the
engine lock would leave the chat dependency in place.

## Decision

Use a resource lock per agent. The global engine lock protects resource state
and operation registration. File access, MCP connection, and resource close
run outside it. Calls for the same agent wait with context cancellation.
Track opens and reloads before resource work starts. Register recovery
isolation before Harness resume. Close waits for tracked work and prevents
replacement resources from remaining open after shutdown.

Keep the global idle precondition for Reload. Check idle state and register
the selected replacement atomically. Reload replaces only that agent's
resources and does not own the global maintenance pause. After preflight
succeeds, other agents can start work while the replacement continues.
Maintenance fails busy during an active Reload. Confirmed maintenance blocks
Reload. `Quiesce` and `Resume` remain maintenance-only operations.

The idle preflight calls native `Inspect` under the global engine lock. These
local storage reads keep the idle check and resource registration atomic. Use
a tracked preflight if those reads start to delay other agents.

Use a delivery lock per agent for input, Stop, Clear, and reconciliation.
A short global receipt gate orders receipt commits and maintenance lease
confirmation. After confirmation, web input returns `409` before receipt
insertion. A web receipt committed before confirmation can return `202` and
remain pending until release. Channel input can retain pending receipts.
Keep the two-minute lease, release and expiry behavior, stable request IDs,
and native admission authority.

Use one application cleanup path for startup failure and shutdown. Stop jobs
and bots before the chat consumer. Close execution resources before business
storage. The chat service owns global source deduplication through
`ReceivedSource`. It scopes preview references to each agent through
`LookupSource` and `SourceReceipt`. Source identities retain deduplication when
a bot credential moves between agents. Channel transports do not define receipt
state from native admission timestamps or submission status.

## Consequences

A slow MCP connection for one agent does not hold another agent's resource
or delivery lock. Work for the same agent remains ordered. Global maintenance
still covers every agent. This decision does not remove the provider discovery
gate or change model configuration.

Business and native stores still have separate commits. A stored receipt does
not prove native admission. Reconciliation remains necessary, and external
tool actions are not guaranteed to occur exactly once.
