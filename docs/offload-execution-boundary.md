# Offload as the execution component

Direction agreed on 2026-11-02. Offload replaces the execution integration
currently provided by the ACP adapter. Agent-Host remains the single Host for
tasks and all conversation entries. This is a staged migration, not a production
switch or a second channel platform.

## Ownership and usage

Users keep sending tasks through DingTalk, Feishu, CLI or Web/Mac. Host owns
identity, authorization, Agent/project routing, context, memory and Skills,
task records, business acceptance and result delivery. Offload owns execution
adapters, native invocation, continuation and cancellation/cleanup. Native
engines retain their own model/tool loops and conversation state.

All entries should call the same Host flow through `harness.Runner`. ACP may
remain an implementation detail of an Offload engine adapter. Memory enablement
is a Host decision independent of the execution backend. Host run IDs remain the
user-facing lookup identity, with Offload execution IDs linked as evidence.

## Current increment

The existing Offload prototype now calls a shared harness adapter instead of
calling the execution client directly. It retains private raw evidence, canonical
continuation bindings and independent execution/memory outcomes. Unsupported
request controls fail explicitly before native dispatch.

Version 2 execution profiles now call Offload's published `v0.6.1/pkg/execution`
in process, without launching the Offload CLI. Explicit version 1 profiles retain
CLI compatibility. There is no fallback between them and no automatic adoption
of an old session. [Configuration details](offload-channel-plugins-detail.md#configuration-and-compatibility)
describe the opt-in transition.

The prototype still owns a separate Host orchestration implementation. The main
Host has not adopted this adapter, the legacy paths remain available, and no
installed service or existing native session is migrated by this increment.

## Delivery order

1. Establish the shared execution boundary and preserve existing prototype
   behavior, then consume the public Go package. **Implemented; headless validation only.**
2. Close the capability gaps needed by the common Host: tools and permissions,
   context/Skills injection, native bindings and real streamed events. Keep
   model-based memory extraction within explicit Host policy.
3. Move Offload selection into the common Host composition and remove duplicate
   orchestration as entries migrate. Prove routing, delivery, continuation and
   memory behavior through the same flow.
4. Accept the isolated end-to-end path, then perform a separately scoped
   production rollout. Remove the direct ACP path only after parity and
   migration acceptance; never silently retry failed work on another backend.

There is no new generic plugin loader, parallel task controller or automatic
native-session migration in this work.

[Implementation contract and checks](offload-execution-boundary-detail.md).
[Current architecture](architecture.md).
