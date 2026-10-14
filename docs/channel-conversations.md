# Conversation execution and attachments

This increment adds concurrent conversations and message attachments. Automated
checks and native/platform acceptance are tracked separately; earlier text-only
acceptance does not validate these additions. See the
[implementation details](channel-conversations-detail.md).

## Everyday use

Messages in one group or private conversation run in order. Other audiences can
make progress concurrently, with up to four active conversations by default.
Conversations sharing the same agent and memory audience also stay serial.
A turn keeps its place until execution, memory processing and reply delivery
finish. `/status` waits in that same queue; it is not an immediate control.
An interrupted progress hint does not cancel the task. The final reply warns
when progress reporting failed; execution evidence and final state still have
to be recorded successfully.

Send text, pictures, files, image-with-text messages, or forwarded Markdown.
One message can contain up to four attachments: each image up to 5 MiB and each file up to
20 MiB. Claude receives images as native image inputs and reads ordinary files
with its tools. Quoted text, a chat-record summary, or an interactive-card
preview (text, title and action link) is used when the callback supplies it.
Missing original content is not fetched automatically.
Forwarded Markdown is supplied as reference material, with long bodies marked
as truncated. Links in forwarded content do not automatically download images.

The [history tools](conversation-history.md) provide on-demand search/read of
saved Agent interactions across native-session resets. They use Host receipts
and exact saved replies without DWS, scoped to the current conversation, Agent,
audience and task projects. Unaddressed group traffic is excluded. The optional
DWS collector remains an independent system path for selected daily chats.

Only standalone `/new`, `/status` and `/help` are Host controls. Commands in
attachments or quoted material are task content. Output stays a text/Markdown
reply; sending generated images or files is outside this increment.

## Configuration and boundaries

Set `max_concurrent_conversations` in the private service configuration, or use
`dingtalk serve --max-concurrent-conversations N`. The supported range is 1–32;
omission uses four. Restart for configuration changes to take effect.

Identity, agent selection and memory partitions remain unchanged. Received
attachments are saved privately under the state home's `attachments` directory,
with their source and content metadata. Callback download credentials remain
outside public records and model input.
Trusted download links returned by DingTalk are upgraded to HTTPS when needed,
so an HTTP link from its file API can still deliver the attachment securely.

Separate conversations do **not** isolate shared project files. Two audiences
can operate on the same checkout concurrently; no worktree or filesystem lock
is added. Use a limit of one when these tasks must be globally serialized.

Unclaimed queued messages survive restart. Claimed work with an unknown result
and replies with uncertain delivery are never automatically replayed. This
increment adds neither asynchronous memory processing nor a `/stop` command.
When a failed conversation needs a fresh native session, the Host supplies the
linked previous question and saved reply as context. For a short follow-up, the
Agent reviews that context and available conversation history before asking
you to repeat the goal. A new independent request keeps its own goal.
Other fresh native sessions also receive a few recent user questions from the
same conversation and authorized project scope, so a model change or reset
does not leave a brief follow-up without its subject.
Associated quote text and source references travel with those questions,
including available preview and truncation information.
Real conversations, test inputs and platform evidence stay outside the repository.

See the [DingTalk guide](dingtalk-channel.md), [instance design](agent-instances.md)
and [installation guide](service-install.md) for the existing channel setup.
