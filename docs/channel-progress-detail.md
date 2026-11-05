# Channel progress implementation

[User contract](channel-progress.md).

Host mounts `channel_progress` only for a persisted Source.ChannelMessageID on an
admitted Channel business run. It enables durable run authority and revokes it
on exit/cancellation. Private stdio MCP opens existing exact-schema SQLite in
read/write mode, with immediate transactions, busy timeout, foreign keys and FULL
sync. It does not migrate, recover, initialize memory or acquire the Host lock.
No provider credentials or network transport are exposed in MCP.

The stable MCP SessionKey identifies the conversation authority. Private arguments
and fingerprints bind each turn to its current receipt/run. Continuations keep
the native conversation identity and rebind current MCP arguments. Managed
catalog usage records the tool contract, guidance and stable authority key.

Schema 18 introduced authorization and progress attempts. Additive schema 19
links each new attempt to a unique nullable reaction_id foreign key. Existing
history is preserved; legacy unlinked pending sends become failed or unknown
without replay. Reservation atomically inserts the quota-bearing attempt and a
channel_reactions row with stage progress:<attemptID>. Event attribution stays
empty/zero; provenance comes from the linked attempt/run, never a fabricated
native event sequence.

One Channel feedback worker consumes lifecycle and MCP reactions. The existing
queue claim serializes each receipt, including across callers. Progress authority
is revalidated at claim, before removal and again before addition using persisted receipt,
run, session, partition, scope, runtime, enabled authority and fingerprint.
Terminal enqueue supersedes queued nonterminal feedback and settles linked
attempts; a terminal barrier forbids later progress dispatch. Already authorized
mutations may finish, then terminal replacement runs last.

Removal is durably claimed before network calls and its outcome saved before
addition. Exact prior custom labels are retained in reaction history. Confirmed
reaction outcomes and linked attempt outcomes commit atomically. Supersession,
skips and recovery also settle linked attempts. Dispatched operations and unknown
removals/additions never replay; uncertainty blocks replacement. Recovery fails
obsolete unclaimed progress and revokes old run authority.

DingTalk fixed lifecycle methods retain their stage whitelist. Separate custom
methods validate the short label and use the existing bot textEmotion add/recall
payload, fixed emotion ID 2659900 and background im_bg_1. Live custom-text bot
acceptance remains unverified; provider failures are explicit with no message
fallback. Feishu maps progress to Typing and uses saved provider IDs for removal.
Unsupported feedback fails. Disabling automatic lifecycle reactions does not
suppress explicitly requested supported MCP feedback.

No progress path calls text reply adapters or changes final reply fields. Final
reply delivery, task completion, acceptance and memory remain independent. Only
sanitized classifications cross MCP or feedback logs; raw provider errors and
URLs are not forwarded.
