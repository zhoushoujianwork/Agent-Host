# External knowledge and everyday experience details

Status: Dokki/Confluence runtime tools, local Markdown sources and audience
filtering are implemented, with separate acceptance recorded in the
[local-source details](local-knowledge-detail.md). Bounded DWS private-chat
retrieval validated; optional system intake and topic review are now implemented
in the [history/discovery details](conversation-history-detail.md), while learning
remains deferred. The
[main guide](knowledge-sources.md) governs scope. Existing source evidence and
historical validation remain distinct from new connector acceptance.
The [Host history tools](conversation-history-detail.md) provide a separate
receipt-bound read-only view of Agent interactions. DWS system topic discovery
and future learning do not expose the personal chat cache through task MCP tools.

## Implementation and reuse

`internal/knowledge` adapts the baseline memgov Dokki and Confluence connectors
into a private Agent-Host MCP process. The installed Host therefore needs no
memgov process, applied-configuration database or checkout. The first design's
external-process bridge was replaced with this narrow independent deployment
path because the existing hidden memgov command reloads mutable applied state.
The [attribution](../internal/knowledge/ATTRIBUTION.md) identifies source revision
`8300e2a535690b2eef2e1ff01b851bb6189942bb`, changes and the retained Apache-2.0 license.
No advanced Confluence dispatcher or export operation was copied.

Agent-Host retains its SQLite memory authority and native permission behavior.
The current experience policy still requires a completed execution run;
knowledge-reference tools are explicitly excluded from extraction input and
candidate publication. Original calls/results remain in the run journal.

| Component | Delivered responsibility |
| --- | --- |
| `internal/knowledge/config.go` | Private local configuration, project selection, source/credential fingerprints, per-call binding validation |
| `internal/knowledge/client.go`, `mcp.go`, `confluence_write.go` | Source HTTP protocols, source-specific dispatch including Confluence writes, MCP stdio, bounded responses and attributed receipts |
| `internal/knowledge/local.go` | Explicit Markdown scope, rooted file access, bounded search and document digests |
| `internal/assembly` | Source catalog and source fingerprint in native session compatibility |
| `internal/harness/claude` | Exact native MCP mounts, strict ambient exclusion and connected-server initialization checks |
| `internal/cli` | `knowledge check`, internal `knowledge serve`, chat/channel/service configuration and per-turn reassembly |
| `internal/host`, `internal/state` | Saved catalog/tool evidence, pre-dispatch binding validation and exclusion from execution-experience publication |

## Configuration and runtime assembly

The version-1 JSON [example](../config/data-knowledge.example.json) contains source
names, `provider`, `endpoint`, optional Dokki-only `source_url_base`, `credential_file`,
`principal`, `description` and explicit `projects`. Files must be regular,
current-user-owned, deny group/other access, and use absolute paths. Unknown or
duplicate fields, duplicate names, malformed endpoints and mismatched credential
types fail locally. HTTPS is required except HTTP loopback for controlled tests.

`local_markdown` uses `root` and explicit relative `paths` instead of an endpoint
or credential. Paths can select files or directories; directory scanning is
bounded and reports incomplete coverage. Local results use source-relative
document references and common digested excerpts. See the
[local-source contract](local-knowledge-detail.md) for validation, private retained
snapshots and the distinction from learned Host memory.

`principal` is an operator-supplied account label, not a provider-verified identity.
The actual token defines upstream visibility. Only the existing local owner and
verified application-bot routes consume this configuration; no upstream multi-user
ACL is claimed. Optional `audiences` narrows source selection in addition to
projects and Agent source subsets. Omission preserves existing behavior;
`["owner"]` prevents a source from being mounted for a group audience. A source's
project IDs constrain where it is mounted, not which
pages the upstream credential can read. `default` applies to unregistered
workspaces. There is no implicit wildcard for all registered projects.

Token files contain only the provider's `api_key` or `pat`; advanced Confluence
`dock_token` is not accepted. The connector reads tokens directly from those
files. The host computes an opaque binding fingerprint from source configuration,
connector policy version and token; it never puts tokens in prompts, arguments,
events or saved state. An echoed connector token in a response is redacted;
this is not general secret classification of document bodies.

Local-source fingerprints include source declarations and grants rather than
document bodies. Editing a document is checked by its read digest; changing the
effective access declaration changes session compatibility. All configured
sources are subject to audience selection before resolution. A local source
root is not automatically mounted as a native execution directory.

`knowledge check --config PATH` validates local source declarations and tokens
without making network calls. Its report includes navigation hints, projects,
opaque fingerprints and `remote_verified=false`. Chat and DingTalk accept
`--knowledge-config PATH`; the service accepts optional `knowledge_config` in
`service.json`. Only selected source credentials are read during task assembly,
so an unavailable unselected token does not block an unrelated project.
Joint runs select the deduplicated union of participating project IDs. The
neutral primary cwd does not add `default` source grants. Navigation hints
retain the selected projects authorized for each source. Full source bindings
participate in the set's session fingerprint and are rechecked before dispatch.
Service/channel preflight resolves an already registered workspace before
selecting sources. Its read-only project check also accepts schema 7, 8 and 9 under the
schema-10 binary with a compatible project projection; it does not migrate or write
state. General state inspection retains its strict current-schema requirement.

Each source mounts a stdio child of the same Agent-Host binary:
`knowledge serve --config PATH --source NAME --fingerprint EXPECTED`.
`harness.Request.MCPServers` holds only names, executable paths and arguments.
Claude receives `--strict-mcp-config` and the exact native configuration,
including an empty set when nothing is selected. Ordinary local chat retains
native policy and explicit `--allowed-tools` rules. Configured Agent/channel
instances use the operator-selected `bypassPermissions` mode described in the
[instance design](agent-instances.md). Instance source subsets narrow existing
project grants before credentials are read; they do not grant new sources.

Initialization must report every configured server as `connected`; missing,
pending, failed, duplicate or unexpected mounts and skipped MCP configuration
are explicit failures. Connection does not establish remote source authentication.
Extraction and `--no-tools` accept no mounts. Ambient native MCP configuration is
excluded even when no knowledge sources are selected. This is a tool integration
boundary, not an OS sandbox for a process with other native capabilities.

Only short selected-source hints and fully qualified tool names enter the prompt.
No document search/read occurs during assembly, MCP initialization or tool listing.
The native agent chooses when to call a source. The `knowledge.assembled` event
records the catalog; paired native tool events retain the actual retrieval.

## Compatibility, revocation and evidence

The runtime fingerprint includes the effective source binding and the strict-MCP
assembly policy. Credential replacement rotates it conservatively even when the
account is unchanged. CLI interactive turns and channel tasks reassemble before
execution. An explicit stale `--resume` is rejected; normal channel binding and
interactive configuration changes start a fresh compatible session.

The host revalidates selected access immediately before dispatch. MCP reloads
local source configuration and credentials before each tool call and rejects a
changed or removed binding. An already-issued HTTP read is allowed to finish;
there is no immediate cancellation of in-flight reads or retroactive erasure of
native history. Upstream ACL/credential revocation is observable only when the
provider rejects a subsequent read; the local fingerprint cannot detect remote
policy changes. No stronger revocation guarantee is claimed.

| Source | Delivered tools |
| --- | --- |
| Dokki | `list_dokki_workspaces`, `search_dokki`, `read_dokki_resource` |
| Confluence | `search_confluence`, `read_confluence_page`, `create_confluence_page`, `update_confluence_page` |
| Local Markdown | `search_local_markdown`, `read_local_markdown` |

Tool names use `mcp__knowledge_<source-name>__<tool>`. Dispatch enforces the
provider-specific set, independently of tool annotations. Unknown tools and
advanced Confluence exports cannot be invoked. HTTP requests use bounded time
and response size, refuse redirects, and report status/connection/timeout errors
without source-response error bodies or credentials. There are no automatic
retries, alternate providers or credential fallbacks.

### Confluence page writes

`create_confluence_page` takes `space_key`, `title`, full storage-XHTML `content`
and optional numeric `parent_id`. It sends `POST /rest/api/content`.
`update_confluence_page` takes numeric `id`, `title`, full `content` and
`expected_version` from a prior page read. It sends `PUT /rest/api/content/{id}`
with `version.number = expected_version + 1`; stale versions fail with HTTP 409.
Updates do not move pages or change spaces. Empty content is allowed when
explicitly supplied. Body size is bounded to 128 KiB before sending. Only these
typed operations are exposed; there is no arbitrary HTTP dispatcher.
The API follows [Atlassian's REST examples](https://developer.atlassian.com/server/confluence/confluence-rest-api-examples/).

The existing source binding and PAT are revalidated per call. Write tools advertise
`readOnlyHint=false`, `idempotentHint=false`, with `destructiveHint=true` for
full-body updates. Connector version v5 changes native session compatibility so
new tool availability is assembled after restart. Writes return compact page
identity, URL and version plus a source receipt marked `coverage=write_response`
and `content_verified=false`. Read back to establish saved content. Transport,
HTTP and malformed-response failures do not trigger a retry; even a failed result
may follow an applied write, so inspect the target first.

Automated coverage includes MCP create/update/readback, Unicode bodies, parent
placement, version conflicts, argument validation, output annotations, bounded
bodies, rejected redirects and explicit failures without retries or credential
leaks. The opt-in live test requires `AGH_CONFLUENCE_WRITE_LIVE=1`,
`AGH_CONFLUENCE_CONFIG` (one configured source) and `AGH_CONFLUENCE_SPACE` (an
authorized test space); it leaves a clearly named acceptance page in that space.
On 2026-10-15, the owner's personal space passed create, exact body readback,
update to version 2, exact updated title/body readback and stale-version HTTP 409
rejection. The retained [test page](https://confluence.zhihuiya.com/pages/viewpage.action?pageId=298113920)
records that bounded acceptance; this does not establish general native-agent
write quality or permissions in other spaces.

Results retain source IDs, human-facing links, bodies and upstream revision,
pagination/truncation metadata when available. Dokki metadata/content identity
and returned version compatibility are checked; Confluence reads check page ID,
type and body. The `_agent_host` receipt supplies source instance/provider,
configured principal label, binding fingerprint, fetch time, content digest and
query/window coverage. Source content cannot replace this receipt. A missing
upstream total/version remains unknown; zero hits establish only the result of
that query/window under that credential. Source URLs identify references, not
permission to access a different source.

Read tools accept Unicode-character `offset` and `limit` (default 8,000,
maximum 12,000). Results contain one content excerpt and compact source metadata,
without a duplicate full-body response. Coverage includes the returned/total
character counts, truncation, `next_offset` when more content exists and
`full_content_sha256`. Continuations require that digest as
`expected_content_sha256`; changed content fails and requires restarting at
offset zero. Each excerpt fetches the upstream content again because these
baseline APIs do not supply a common immutable snapshot. The digest identifies
the returned upstream representation after connector-token redaction, not a
provider-wide version guarantee. Non-string content uses a serialized JSON
representation; an individual excerpt need not be valid standalone JSON.
Search/list results retain compact identities, bounded snippets and available
paging metadata. Encoded tool results have a 64 KiB ceiling; oversized metadata
fails explicitly. This keeps ordinary reference reads inspectable in Host
evidence instead of depending on native private output files.

Knowledge calls and native tool-catalog discovery stay in operational evidence but cannot be cited as support for
`reviewed_experience`: extraction filters the reserved mount namespace and native
`ToolSearch`; the store rejects such candidate references again at save/review. A knowledge-only
run therefore has an empty experience result. Mixed tasks may still propose
lessons from actual non-knowledge execution observations. Source documents and
assistant prose do not prove a repair succeeded. Document-derived durable memory
and source-withdrawal governance for those future memories are outside this slice.

## Validation of the implemented slice

`make check` covers configuration, selection, private files, stale bindings,
source/tool isolation, HTTP failures/coverage, native initialization, Host catalog
assembly, revocation before dispatch and memory-source exclusion. Connector and
native protocol fixtures are separate from live remote/model acceptance.
The [live acceptance runner](../experiments/knowledge/README.md) retains private
raw evidence outside the repository and writes only structural report data.
`make check` passed on an isolated source snapshot containing this increment.
The six affected package race suites passed 467 cases; after tightening the
Dokki-only citation-base validation, the full check and the connector's 84 race
cases passed again. Concurrent runtime-phase/schema changes were excluded.
After runtime-phase commit `ee88bbd` landed, a second isolated combined snapshot
passed `make check` and 482 race cases across the same six packages. The added
project-preflight compatibility test uses genuine schema-7 data; the combined
binary also successfully checked the installed schema-7 service without migration.
Uncommitted intelligent-routing development was excluded from this verification.

The first real run exposed an oversized Dokki result that Claude externalized;
Host evidence could not retain its complete receipt. The corrected connector's
second live run (private validation evidence)
passed: seven paired successful source calls, both sources searched/read/cited,
scoped empty queries, and a digest-pinned Dokki continuation. It produced zero
experience candidates and did not invoke the extractor. Only the requested
excerpts were read; this does not prove complete search coverage or general
answer quality. The [runner notes](../experiments/knowledge/README.md) retain
the original failure and identify the later configuration-only validation change.

The production installation (private validation evidence)
at 08:39 UTC on 2026-10-10 enabled both sources for the existing default workspace,
with private credentials and five explicit native tool permissions. The installed
binary passed local checks and established a fresh DingTalk Stream connection;
schema 7 and existing channel outcomes were preserved. Native-source acceptance
and service startup were verified separately; a new DingTalk knowledge-question
round trip has not been observed. DWS intake remains disabled.

## DWS private-chat retrieval acceptance, 2026-10-11

Manual read-only queries through DWS CLI `1.0.61` used one fixed, authenticated
personal profile. The structural report (private validation evidence)
and [method](../experiments/dws/README.md) record this separately from Agent-Host
integration. No listener, message send, intake worker or memory extraction ran.

The target was a real colleague's conversation, confirmed by
`conversation-info` as `singleChat=true` with two members. History returned six
messages, including four from the owner and two from the colleague. A
`+search-msg` query with that actual conversation ID, the resolved owner as
sender, an explicit historical day and a keyword returned the same owner-sent
message seen in history. `+messages-mget` reread the same ID and content.

`+chat-messages` followed returned pagination information for two pages, then
reported `complete=false`, `hasMore=true`, `stopReason=page_limit` and
`truncated=true` at the configured limits. A unique nonmatching keyword returned
zero results with `complete=true` for the selected conversation and day. A
recent history window starting on October 4 was empty while September 28 had
messages;
the CLI's default seven-day search window cannot establish absence across history.

This proves keyword search and bounded history retrieval, including the owner's
messages. It does not prove semantic search, enumeration of all private chats,
complete historical coverage, edit/retraction handling, event-stream coverage,
continuous intake or lesson quality. The sampled conversation was located from
a known counterpart identity. The current conversation-list sample exposed
groups and did not establish exhaustive private-chat discovery. Search applies
exact conversation filtering to a bounded source scan; pagination and coverage
metadata must remain part of any future import. Raw evidence stays private
outside the repository; the positive search was inspected interactively.

## DWS observation and episode intake

Keep the application-bot task channel and personal DWS observation independent.
The former accepts direct instructions addressed to the bot; the latter collects
selected source evidence without executing requests or sending replies. An
identity mapping may associate the accounts, but their credentials, message IDs
and permissions are not interchangeable.

Bot conversation recall uses the Host's retained receipts/replies through the
separate history MCP. Implemented DWS collection uses selected conversations,
bounded reconciliation, immutable evidence and deduplicated topic proposals;
its exact current limits and CLI are in the [implementation guide](conversation-history-detail.md).
Local topic acceptance exports owner-private evidence for its explicit registered
project. It starts no task, sends no notification and publishes no memory.
The remaining episode-learning and task-handoff design below is planned.
Cited immutable snapshots survive transient cache expiry for retained topic review.

The local DWS event contract documents that `event +listen-im` filters the
authenticated user's own sent messages. Event-only observation would therefore
miss the owner's solutions. Current collection uses bounded selected-source
history polling and reconciliation. Incoming/change events remain future work.
Do not describe history polling as loss-free realtime listening. The installed
account's coverage and supported change events need acceptance before rollout.

`chat +chat-messages`, `chat +search-msg` and related reads use
`im.message-list.v1`. Preserve `queryRange`, `resolvedFilters`, `complete`,
`partial`, `hasMore`, `nextPage`, `failedCount`, `failures` and truncation reasons.
Continue only with genuine returned pagination information. The references are
the local DWS `multi/dingtalk-event/SKILL.md` and
`multi/dingtalk-chat/references/contracts.md`; verify exact installed leaf
schemas during implementation. The original design did not access a DWS account;
the separate retrieval acceptance above validates only a bounded live sample.

Persist each message before analysis: tenant/principal, provider conversation
and message IDs, source revision or content digest, author, source timestamps,
thread/quote references, content and availability. Use provider identity plus
revision for deduplication; identical text in separate conversations is not the
same evidence. Overlapping imports/events must not create duplicate candidates.
Link a bot receipt to an observed DWS message only when stable provider identity
proves they are the same source; otherwise retain separate observations.

History coverage and job progress are separate. Commit evidence and its local
checkpoint together; retain failed/partial ranges for bounded reconciliation.
The dws process receiving a cloud event does not prove Agent-Host committed it.
No upstream acknowledgement/replay guarantee is assumed. Authentication failure
pauses that collector visibly without changing the app-bot channel's task state.

Build bounded problem episodes from thread/reply links, explicit issue/project
references and nearby context: question, diagnosis, proposed steps, attempted
steps, reported outcome and unresolved points. Time proximity alone must not
merge unrelated problems. A quiet window may trigger extraction but cannot
establish success. Ambiguous project mapping or missing cited context holds the
episode for classification/completion; it must not default to the active task's
project. Incoming corrections reopen the affected episode revision.

The first learning scope is problem-solving episodes in the owner's selected
direct chats. Procedures and outcome reports may come from the owner or peers;
each claim retains its actual author. This extends beyond memgov's owner-only
`learn` admission rule and needs separate validation. Selected-group learning
can follow. One-off behavior does not establish a stable personal preference or
habit.

## Selection, provenance and access

Introduce a private source envelope that can reference an existing execution
run or a conversation episode. Existing run-linked records keep their meaning.
New background jobs own their extraction/screening/publication state independently;
they do not create a fake completed run or change business task acceptance.
Adapt candidate inspection, publication, evidence links, retry, comparison and recall
together. Reuse Jev orchestration with a conversation-specific support policy;
the current tool-evidence thresholds do not establish chat-extraction accuracy.

| Evidence | What a proposed lesson may say |
| --- | --- |
| Proposed fix only | The participant suggested these steps; result unknown |
| Participant says it worked | A resolution was reported by the named source |
| Peer confirms the result | Corroborating reported outcome, still not an observed test |
| Paired execution/tool observations | What that specific call/result actually establishes |
| Read receipt, reaction or silence | No conclusion about problem resolution |

Tool-free extraction produces cited claims, conditions, steps, reported outcome,
limitations and exact message revision references. Missing resolution cannot
produce a resolved-case claim. Screen for support and reusable value, compare
existing memory and apply Host validation and blacklist checks, then publish
selected lessons automatically under the [revised memory policy](experience-memory.md).
A separate conversation-derived source type preserves provenance through reuse;
its exact name belongs to implementation. Jev selection does not upgrade reported
experience to verified truth. Existing local topic acceptance remains a source
export operation, separate from future memory selection.

Reuse expiry, invalidation, versioned publication and conflict quarantine.
Compare against currently eligible memory in the same permitted scope/audience;
also consult scoped exclusion anchors so equivalent blacklisted lessons cannot
return through extraction, deduplication or retry. These anchors never enter recall.
Equivalent lessons across source types may be related without merging their
evidence strength. Link later execution evidence explicitly, preserving which
claims it supports. An extraction retry reprocesses saved source revisions and
does not send messages, rerun an external operation or silently fetch newer data.

Conversation evidence and its derivatives begin owner-private. Project relevance
and allowed audience are separate checks. Apply visibility before search,
comparison, model extraction, inspection and context assembly; the extraction
model/provider must also be permitted to process that source. Require both
project applicability and permitted audience for recall. The first implementation
can use owner-only partitions; it must not imply delivered multi-user access
control. A future shared derivative needs explicit audience approval and suitable
content, without disclosing private citations to its recipients.

Track message revision dependencies. An edit, retraction or loss of source access
holds affected pending candidates and withdraws dependent memory from recall
until reassessed; changed evidence requires fresh selection and validation, with
no human memory-acceptance gate. Recheck access
before reuse where the source supports it. If access cannot be established,
exclude the derivative rather than assume that the original read is sufficient.
Detected withdrawal also invalidates native session reuse as described above.
Observation gaps remain visible; immediate detection of every upstream change
is not promised.

Raw retention expiry is distinct from a source retraction. Keep only authorized
cited excerpts under an explicit evidence-retention policy; if required support
is no longer inspectable, suspend the dependent lesson. Any retained tombstone
or audit hash must not preserve content that was required to be deleted.
Neither chat nor document text can grant tool, identity or publication authority.

## Code ownership and acceptance

Extend `internal/assembly` for source selection and compatibility, `internal/harness`
for a private mount description, and `internal/harness/claude` for native launch.
A private knowledge connector owns source protocol/result normalization. Add a
separate DWS collector below a source-ingestion component; keep it out of
`channel` task dispatch. `internal/experience` owns extraction and selection semantics;
`internal/state` owns source revisions, jobs, access partitions and atomic
publication. Background writes must use the existing single-host ownership and
serialized store path; a second process must not bypass the service's home lock.
Exact tables and commands can be specified during their implementation increment.

| Increment | Acceptance evidence |
| --- | --- |
| MCP assembly | Both sources advertise only the baseline tools; no search/read before agent calls; extraction receives none; unselected instances cannot be invoked |
| Native retrieval | Real Claude searches and reads a page from each source, retains IDs/links and reports auth, timeout, partial and zero-hit results distinctly |
| Session and access | Changed source/principal/policy rotates the session; revocation blocks reuse; secrets stay out of prompts, arguments and logs |
| DWS intake | Owner-sent history is collected; stable identity is verified; duplicate event/history reads, restart, partial pages and gaps retain correct evidence/coverage |
| Reported experience | A selected conversation yields a cited lesson, automatic Jev admission/publication and appropriate fresh-task recall without human memory acceptance; advice-only cases never claim resolution |
| Lifecycle | Unselected/conflicted/expired/blacklisted/out-of-scope records stay excluded; equivalent re-extraction cannot bypass a disable; edits/retractions/access loss withdraw dependent knowledge; retry never executes or replies |

Keep deterministic fixtures separate from live source/account acceptance.
Measure useful reuse and false claims with reviewed cases before enabling
continuous intake. Source-level reuse in memgov is not Agent-Host integration
validation.
