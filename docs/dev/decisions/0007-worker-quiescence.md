# 0007: Worker quiescence before service updates

Status: accepted. Date: 2026-10-15.
Supersedes the maintenance gate decision in [ADR 0006](0006-service-and-update.md).

## Context

Control dispatch reservations do not cover all worker execution. Opening a
harness for a browser watch can resume persisted work after a restart.
A settled event also precedes the memory commit. A successful ping proves
neither that tools are idle nor that memory writes have finished.

## Decision

Keep the control dispatch gate and add worker `quiesce` and `resume` RPCs.
Control closes its gate before it asks the worker to quiesce. The worker
closes admission before its first await, then checks active RPC operations,
settle work through the end of its memory commit, and each open harness.
Use pi-durable's `Harness.inspect()` for unfinished tasks and submissions.
Do not infer run state from chat events or copy the scheduler's logic.
Reject acquire as busy if any work remains. A failed acquire releases both
gates. Control serializes lease changes without holding its dispatch lock
during an RPC, because the stdout reader can process a settled event.

While gated, new submit, create, watch, and abort operations wait before
opening a harness. Existing watches can continue to read. A new watch waits
in full; opening a paused harness adds a second recovery path with no benefit
for a short maintenance window. Both gates use the same owner and expiry.
Waiting worker operations recheck renewed leases when they wake. Worker
initialization restores a live lease before watches or replay can reach it.
A failed restore prevents publication of the new worker.

The worker's stdin uses a real pipe with cancellable writer admission.
Cancellation sets a write deadline. A failed partial write closes the pipe,
because another JSON request cannot follow an incomplete protocol line.
Health retains its one-second worker budget. CLI probes allow two seconds
for that server response.

## Consequences

A watch can wait until release or lease expiry. New messages remain in
control storage. Updates cannot restart dex while resumed durable work or a
memory commit is active. A crashed worker cannot remove a held lease.

Real-worker tests kill a worker with a bash run and a queued input, then
open a watch without control replay. pi-durable does not rerun an unsafe
interrupted bash call; it can resume the queued input and execute its tool.
A real git pre-commit hook holds a memory commit after settle. A real full
pipe proves that RPC cancellation covers both writing and writer admission.
