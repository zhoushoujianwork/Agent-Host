# Chat through Offload

Use the same slim execution host for local Chat and the DingTalk/Feishu plugins.
Offload owns native execution and conversation history; AGH owns scoped memory
and durable outcomes. This is the foundation for one Web workspace embedded in
the Mac app, with no second model loop.

The delivered first step supports `chat -m --offload-config PATH` and explicit
`--resume SESSION_ID`. Optional memory uses the existing profile. An answer
appears before memory closeout; a failed memory operation can be retried without
repeating native work. Direct input is attributed to the local owner, without
inventing a channel message or channel sender.

Web/Mac room requests are not connected in this step. Interactive input, room
requests, channel takeover, automatic runtime renewal and managed tool injection
fail explicitly. Existing channel and legacy Chat paths retain their behavior.

Next: integrate the existing Web room model, preserving request deduplication,
compatible native continuation, concurrency and cancellation. Production adoption
and live provider acceptance remain separate from source implementation.

[Configuration and implementation details](offload-chat-detail.md).
