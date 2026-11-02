# Offload execution boundary — implementation notes

[Main direction and delivery scope](offload-execution-boundary.md) owns the
product boundary and migration sequence.

## Current adapter

`internal/harness/offload.Adapter` implements the existing `harness.Runner`
interface. It delegates once to `internal/offloadexec.Client`, which owns CLI
validation, private instruction-file cleanup and process-group termination.
The adapter never reads Host state, routes a task, retries work or switches
engines. `internal/offloadchannel.Host` now uses this adapter; its existing
CLI/Web/channel entry points and configuration retain their current behavior.

The harness request carries Host caller correlation, visible prompt, separate
Host instructions, workspace, explicit write intent and a recorded continuation
session/profile pair. Write intent is not authorization or a sandbox. Fresh
requests cannot adopt a supplied native binding. A resumed request must carry
both the native session and its canonical profile; Host still verifies the
binding event and compatible runtime before invoking the adapter.

Normalized results use `completed`, `failed` and `canceled`. Failure categories
remain startup, deadline, cancellation or process failure, retaining the
original error chain. Invalid output and executor errors cannot become success.
Optional execution evidence retains Offload run/caller IDs, canonical profile,
backend, original status/error kind, usage and raw record. Host persists this
evidence privately and keeps its current `offload.result` and `offload.binding`
events; presentation observers do not receive the raw record. Acceptance is not
inferred from execution completion or from saved memory.

The current CLI supplies one final record. The adapter can emit a final result
observation; it does not invent text deltas or tool activity. Observer errors
are returned without changing a completed execution into a failed execution.
The prototype keeps its existing observer/audit behavior and private journal.

## Explicit capability gaps

Before dispatch, the adapter rejects per-request native profiles, permission
modes, tool disablement/allowlists, restricted execution, MCP servers, structured
schemas/extraction, native Skill packages, model overrides, images, additional
directories and true session forks. Profile-level model selection continues in
the existing client configuration. Read-only intent cannot replace restricted
execution. Unsupported controls are not converted to prose instructions.

The common Host currently assumes a preallocated native identity and assembles
MCP/Skills and memory extraction capabilities. It must not select this adapter
until those contracts are implemented or explicitly separated. The prototype
also has its own room-continuation rules; these are retained, not generalized
into support for native forks. No public configuration is added in this slice.

## Validation and migration acceptance

Headless adapter tests cover fresh/resumed request mapping, context separation,
private evidence, caller correlation, failure categories, pre-dispatch rejection,
cancellation and observer failure without replay. Existing prototype and CLI
tests exercise real fixture processes, native binding, room deduplication,
cross-entry restrictions, optional memory and answer-before-memory behavior.
`make check` is required before the local commit.

Later migration acceptance must demonstrate the common Host path for Agent and
project routing, provenance, tool/permission enforcement, Skills/context,
streaming, native continuation, cancellation cleanup, result delivery and
independent memory/acceptance outcomes. Live channel and desktop validation
require their own explicit scope. Isolated native evidence does not authorize
or establish production adoption.
