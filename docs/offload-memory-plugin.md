# Optional memory for Offload channels

Keep useful user declarations across new native conversations. Offload continues
to own agent execution and conversation history; Agent-Host owns scoped memory,
its evidence and whether a statement is eligible for recall.

Enable `memory.enabled` in the channel's Offload profile. Local retrieval works
without another model. Optional processing uses the existing Jev classifier to
classify the exact authenticated user statement, check duplicates and conflicts,
and publish it through AGH's existing memory store. No extra Offload worker or
legacy Claude extractor is started.

The answer is delivered before memory processing. A failed memory operation has
its own visible outcome and can be retried without rerunning the user's task or
losing the compatible native session. Disabled memory keeps the previous path.

This increment supports source-backed user statements, bounded recall, conflict
quarantine, and invalidation. It does not learn procedures from assistant output
or tool results. Quoted material and ambiguous multi-message input are excluded
from automatic publication. Invalidating AGH memory excludes future retrieval;
it does not erase the separate native conversation history.

Acceptance uses fake execution and local classifier fixtures: store a fact,
start a fresh native session, verify attributed recall, then invalidate it and
verify exclusion. Also check audience/Agent isolation, duplicate writes,
conflicts, disabled/retrieval-only modes and failed-processing recovery.

[Configuration, state and validation details](offload-memory-plugin-detail.md).
