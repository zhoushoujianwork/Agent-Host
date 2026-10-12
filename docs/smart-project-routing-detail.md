# Intelligent project routing: implementation notes

The [main design](smart-project-routing.md) governs scope and user behavior.
Registered-project routing, project sets and default-workspace execution are
implemented in source. Earlier waiting requests retain an explicit compatibility
path. The validation section keeps automated checks,
real native execution and live platform acceptance separate.

## Router contract and evidence

`internal/routing` runs before assembly, memory retrieval and native execution.
Its private `Decider` accepts the exact request, registered `state.Project`
descriptors and recent task references ordered newest first. Each recent task
retains a source message or run ID, its project set and optional primary project.
The old explicit `Resolve` interface remains available to CLI operations.

The decision returns `project`, `default` or `clarify`, selected/candidate IDs,
an optional primary ID, reasons and source evidence. A continuation returns its
source message/run IDs for channel-side compatibility checking. Paths, scopes,
credentials and native transcripts are not classifier inputs or outputs.

For a new channel task, only a reliable project association selects a registered
environment. A valid `default` or unresolved `clarify` result executes in the
already selected agent's default workspace. The latter remains routing evidence,
not a question to send or a waiting record to create. The native agent may ask
for business details during its ordinary conversation. An empty catalog skips
the classifier and directly resolves the default environment. This policy does
not reinterpret provider errors as uncertainty: invalid output, provider failure,
invalid configuration and unavailable selected directories still fail explicitly.
Default execution neither attaches guessed projects nor broadens knowledge access.
The channel retains the original decision for audit before default handoff.
Handoff does not force a new default session: a trusted per-turn instruction
requires the native agent to avoid treating unrelated earlier context as the
current target.

Exact affirmative IDs, names and aliases can resolve directly. An alias may
contain a repository reference, but there is no remote lookup. Token boundaries
avoid matching a short ID inside a longer identifier. Project negation, exclusions,
quoted/reference-only mentions and shared aliases require semantic classification.
Actual comparison or integration of two projects can select both.
Complete execution constraints such as "do not modify files" or "不要保存为记忆"
do not negate an otherwise explicit project; the original request remains intact.
Exact names alone do not establish execution intent. The provider-free path
requires a complete project selection or affirmative engineering command;
ordinary language operations on project names go through semantic routing.
Bare service domains, URLs, repository remotes and network addresses also receive
an additional check before direct selection. A complete registered hostname or
repository alias can cover a reference only when its project already appears in
the exact match set. An
unmapped address, or a known address whose project did not match directly, sends
the entire request to semantic routing. Short IDs do not imply ownership of a
subdomain, and one known target cannot hide a second unresolved target. This
guard does not itself select a project or grant access.

Semantic routing uses the configured tool-free Jev client. Independent questions
cover task mode, ownership, whether the requested action needs an engineering
project, each project's relevance, its evidence category and the primary project.
Policy `project-routing-v6` evaluates the action rather than treating technical
words inside a quotation, communication request or information lookup as an
engineering operation. Registered-project selection still requires consistent categorical
answers, valid registered identities and sufficient probability; numeric
confidence alone is not a route. Purpose-based selection also requires an actual
description or alias. For non-target projects in an otherwise resolved project
task, unrelated and excluded probabilities may combine; related probability
and affirmative evidence cannot be used to dismiss an unresolved target.

Policy v6 distinguishes reasoning from supplied task materials from accessing
an engineering environment. Explaining pasted logs, code snippets or tables,
including likely causes and suggested verification steps, can be independent
general work. Project names and technical symptoms inside those materials are
context, not affirmative access requests. Fetching live deployment logs or a
target project's own implementation, API specification or configuration requires
reliable project evidence before the Host selects that project's environment.
A no-write constraint does not supply that evidence. Unresolved requests execute
in the default workspace, where the native agent can ask for missing details.
The same distinction applies with recent engineering context:
independent material analysis must not inherit it through topic overlap, while
a grounded request to continue that investigation retains the verified source.
This prompt change keeps the 0.90 reliability threshold and categorical decision
rules. Aggregate probability comparisons allow a `1e-12` tolerance for floating-point
addition rounding at that boundary; a total of 0.8999 still fails. Single-answer reliability
and independent scope checks retain their thresholds. Material keywords do not
establish a project association. These classification checks govern project
selection, not whether an unresolved request may start native execution.

Policy v4 clarifies two independent intents without changing routing thresholds
or the decision interpreter. Asking about the assistant's general development
or progress-reporting capabilities is ordinary conversation, not an instruction
to inspect or change an unspecified codebase. Asking to list or count folders
or repositories under a user-specified local parent, locate their paths, or
check their existence is a metadata inventory that can run in the default
workspace with existing native permissions. The catalog may be empty. An
inventory does not register its results, select every discovered repository as
an execution target, grant access, or authorize cloning or modification.

These boundaries depend on the requested action, not question wording or
technical keywords. "Could you fix the invoice endpoint failure?" requests an
engineering operation despite its polite form. Reading a project's implementation,
configuration or requirements, running tests, changing code, or investigating
development progress across repositories requires reliable evidence before the
Host selects a complete project set. Otherwise, execution starts in the default
workspace with existing permissions. A directory count alone does not authorize
those subsequent actions.

With recent context, separate `latest_relation` and `scope_change` questions
establish whether the latest source-linked task supplies an omitted subject and
whether its complete scope is preserved. Both must support continuation before
the Host reuses its project set, primary and source references. Contradictory
ownership, excluded members or a new target prevent continuation. The mode need
not literally say continuation: looking in a different source can be information
work while still refining the same project task. Vocabulary overlap, an older
task or recency alone cannot establish this relationship.

For independent conversation, `project_need` must reliably establish that no
engineering project is required. Association must choose general and primary
must choose none; these categorical checks do not add a second probability
gate to the independent need assessment. Every project still requires sufficient
non-target probability and the absence of an affirmative evidence category. With recent
project context, the current request must also be reliably classified as independent.
Non-target probability may include `related` as well as `unrelated` and
`excluded`: a tool's repository or a quoted project
can be related to the content without becoming an execution target. Sufficient
combined mode probability may split between general work and missing
conversational details. When the independent continuity question reliably rules
out a follow-up, the broad mode may also assign probability to continuation
without reintroducing that already-resolved uncertainty. A project mode category
or excessive project-mode probability prevents an independent-general
classification; an unresolved result now still executes in the default environment.
Policy v5 also allows ordinary follow-ups when the latest task already used the
default workspace. With reliable no-project evidence and the same no-target
checks, uncertainty between a new conversation and a follow-up does not change
the execution environment. The broad mode may choose continuation in this case.
This does not assert a source relationship: continuation message/run references
are recorded only when the separate continuity checks establish them.
These checks preserve strict evidence requirements for selecting registered
projects. They do not impose a Host clarification gate. No-match engineering work
starts in the default workspace without being relabeled as verified general work;
the native assistant determines which task details it needs. This is a normal
execution policy, not a provider fallback.

The request is bounded to 16 KiB, the projected classification state to 48 KiB,
the catalog to 50 projects and recent context to five tasks. Limits fail visibly
without truncating away a competing project. Invalid provider output and provider
failure return errors; they never select another provider or the default workspace.
Persisted semantic evidence includes the bounded input, questions and response;
project metadata is source material, never execution authority.

## Selection, continuation and multi-project assembly

Every new channel task uses automatic routing. Historical manual selections are
preserved without being consulted for dispatch. Conversation controls use the
latest actually executed project set, or the configured default when none exists.
A new explicitly named target outweighs earlier automatic choices. A grounded natural
follow-up can reuse the latest source-linked task without fixed continuation
words, including a change of method, information source, constraint or output.
General new topics do not inherit a project merely because it was last used.

| Interface | Behavior |
| --- | --- |
| `project add/update --description TEXT --alias TEXT` | Add a purpose and replace aliases when supplied; omitted metadata is retained. |
| Repeated chat `--project ID` | Execute one task with a registered project set. |
| Chat `--primary-project ID` | Choose the primary cwd from that set. |
| `/new` | Reset the current compatible native session and clear follow-up routing context, preserving memory. |
| `/status` | Show a safe conversation/recent-task summary and any legacy pending count for the requester. |
| `/help` | List these three Host controls and direct task usage. |

Local CLI selection and catalog maintenance remain available. Chat does not
configure project pins or routing modes, and does not forward native interactive
commands. Legacy project/routing/choice commands only explain the current interface.

A single project uses its own cwd. Multiple parallel targets without an explicit
primary use `~/workspace`; an explicit primary uses that project's cwd. All other
selected directories are passed through native additional-directory support.
Assembly validates every canonical path before launch. It retains every selected
identity and memory scope; the model cannot invent additional directories.

The native binding includes the authenticated channel identity, sorted project
set, each directory, primary cwd and shared effective configuration, including
knowledge-source access. A, B and A+B have separate sessions; A+B and B+A describe
the same set. Changed directories, primary selection or effective configuration
rotate compatibility. `/new` never erases Host memory or replays earlier work.

Recall queries each selected project's scope independently and preserves scope
labels in the assembled context. Joint tasks do not recall unrelated default
workspace knowledge. Automatic user-statement storage, execution-experience
extraction and their automatic retries are paused for multi-project runs.
No joint memory namespace is created and no fact is copied between projects.
Single-project/default runs keep the existing memory loop.

New clarify-to-default decisions use `channel-native-handoff-v2`, retaining the
original classifier decision while selecting normal default attribution.
Default follow-ups use `channel-default-continuation-v2`: preserve the original
decision and validate its terminal source, conversation, binding, partition and
provenance before using normal default memory, including after an old unresolved
task. This does not force a new compatible native session.

Historical `channel-native-handoff-v1` decisions and unversioned continuations
retain their saved `MemoryAttribution=unresolved` semantics and skipped learning
records. Readers can consult those turns only under the same saved default-scope
history grant. No old run is rewritten, re-learned or replayed. Invalid versioned
audits or mismatched continuation evidence fail explicitly.

## Existing pending request compatibility

New requests never enter Host project waiting. Existing choice records retain
their question ID, original receipt, candidates and routing evidence without
automatic cancellation, reinterpretation or replay. The following confirmation
path exists only to let the original requester explicitly finish or cancel such
a historical request. Waiting is not execution completion, a native claim or
successful memory finalization.

Only "确认 <任务编号> <项目名称>" or "取消 <任务编号>" identifies a legacy
request for action. Separate multiple project names with `、`; a confirmation
may include a later constraint after ` -- `. Catalog names, IDs and unique aliases
remain valid in that explicit form. Bare project names, "取消" and ordinary
answers are not intercepted, even when one old request is pending. They remain
native conversation input so an answer to the agent cannot execute an unrelated
historical task. Generic assent, new task text or negation does not authorize a
legacy claim. An explicit repeated confirmation cannot replay a resolved task.
Pending lookup filters by original sender before its result limit.
An empty catalog does not block a new task or ask for registration. New requests
execute independently while a historical project task waits.

Confirmation must come from the same verified account, tenant, sender and
conversation. Revalidate selected project identities, directories and runtime
configuration. Claim the waiting task once and persist its environment before
launch. Numbered confirmations retain this path; no synthetic command is
written over the user's answer. Retain the original request unchanged as run input;
retain the exact later constraint as a separate, source-linked user supplement. Do not fabricate
a combined user statement for memory processing.

The clarification reply belongs to the original receipt; final delivery uses
the explicitly linked confirmation receipt and its fresh reply capability.
Question, final-result and reaction outcomes remain independently inspectable.
Duplicate confirmations cannot create a second run. Pending questions survive
restart; claimed work whose result is unknown retains the no-replay rule.
Cancellation is explicit; this increment does not promise automatic expiry.

Correcting a completed request does not silently undo or replay its operations
in another project. A new instruction or numbered pending confirmation must
identify the intended subsequent work. No automated rollback is implied.

## Catalog, defaults and compatibility

Project descriptions and aliases extend the existing immutable ID and memory
scope. Each project still has one current registered directory. Catalog writes
use the existing service lock; stop the service to add, relocate or edit metadata.
No project directories, branches or worktrees are created by discovery because
discovery is not part of this increment.

The configured default cwd remains authoritative for tasks without a reliable
registered-project association, including an empty catalog.
Managed production retains `~/.agent-host/production/workspace` and its historical
memory scope. Creating/validating the neutral `~/workspace` for joint work does
not move production files, change that default or transfer knowledge.

Migration adds routing selections, choice records and source-linked project-set
metadata to the latest merged schema. Existing run inputs, scopes, candidate and
memory revisions, evidence and native histories retain their meaning. Missing
new fields on historical records are read compatibly without rewriting evidence.
Receipt queue identity remains separate from the resolved execution environment.
The former manual selections and pending choices remain unchanged as historical
data. Removing chat project settings and the new-task clarification gate needs
no rewrite or removal of those records.

Later discovery may read selected local roots and the official
[GitHub repository API](https://docs.github.com/en/rest/repos/repos?apiVersion=2026-03-10)
or [GitLab Projects API](https://docs.gitlab.com/api/projects/). Repository
identity must include host and namespace; accessible/recent repositories are not
automatically active. Prefer linking existing checkouts. Preparing a missing
checkout will be a separate explicit import operation with a concrete repository
and destination. Never select credentials from repository text.

## Validation and acceptance

The 2026-10-11 direct-handoff revision passed `make check`, including formatting,
Go vet and package suites, 20 installer tests and 29 viewer tests. Regressions
cover empty catalogs, uncertain classification, unchanged explicit failures,
legacy numbered choices, shared-session continuation and unresolved memory
attribution across restart and retry.

After `make install`, a bounded live group consultation with an empty production
catalog reached Claude directly, searched both configured knowledge sources,
read a source document and returned a sourced answer. The independent event
listener observed the reply, including the question quotation and sender mention.
No project-selection question was emitted; historical waiting requests were not
replayed. This proves one group consultation, not every populated-catalog or
private-chat path. Real input, reply and platform evidence remain outside the
repository.

The records below preserve acceptance of earlier revisions. Some enforced a
project clarification gate or used former project-switch and explicit-choice
commands. They are historical evidence, not validation of the current policy
that starts unresolved requests in the default workspace. Current automatic,
native and platform checks must be recorded separately.

Synthetic supplied-material regressions cover catalog names in log/code/table
content, read-only access to known or unresolved deployments, independent
analysis after an engineering task, and a real scope-preserving follow-up.
They also keep uncertain project-need evidence from selecting the default
workspace and verify aggregate 0.90 boundaries without admitting below-threshold
answers. The controlled evaluator verifies routing boundaries and evidence
handling, not semantic-model accuracy. Real classifier and platform checks are
recorded separately outside the repository; no real messages or test inputs
are included in these regressions.

The policy-v4 check uses independent requests rather than follow-up scenarios.
Two reported inputs passed as standalone messages with empty and populated
catalogs and with their original ambient context (six decisions). Nine additional
independent positive/negative cases passed against both catalogs (18 decisions),
including concrete repair requests that still require a project. `make check`
passed. Private source messages, fixtures and full classifier responses remain
outside the repository, including the initial failed prompt revision.

After installation, two independent owner-DM requests both selected the default
workspace with policy v4. The capability question received a normal answer. The
directory inventory reached Claude but did not complete: native permission
checks rejected access to a parent outside the session's working directories.
The final reply reported the failure; successful dispatch and reply delivery
are not successful inventory acceptance. The first reply and completion reaction
were observed in the event stream; the second reply was verified once from the
completed historical window after the bounded listener expired. Host reaction
states remain separate evidence. The listener left no consumers or subscriptions,
and earlier waiting requests were not replayed.

This exposed a remaining execution capability gap under the then-current native
policy. The prompt-only patch did not relax permissions. The owner subsequently
tested a shared root-directory grant and then replaced it with an allowlist of
the existing `~/github` and `~/gitlab` trees, configured independently in the
[dedicated native profile](service-install-detail.md#directory-access-policy).
That guide preserves the earlier grant's acceptance as historical evidence.
Project registration, memory membership and native command approval remain
separate; the earlier failed inventory evidence is preserved.

The 2026-10-10 policy-v3 repair passed `make check` and 488 routing/channel
race cases. Final live classifier/assembly replays passed 14 scored historical
inputs with an empty catalog, 14 with a proposed local catalog, a two-turn
source-changing follow-up, and 18 independent synthetic boundaries. Seven
historical inputs per catalog and two underdetermined synthetic inputs remain
unscored. Earlier failures and provider-invalid responses are retained in the
private evidence; the latter fail explicitly rather than executing.
Real conversation samples and replay reports stay outside the repository;
committed regressions use synthetic inputs. These replays do not execute the
historical business tasks or establish native/platform acceptance.

A separate production installation restored the Stream connection and completed
three harmless owner-DM turns: drafting, a shorter follow-up in the same native
session, and requesting missing quoted content. All three final replies and
completion reactions were independently observed through the live event stream;
Host reply and reaction states were recorded separately. The production catalog
remained empty, so this platform smoke test validates ordinary conversation and
continuity rather than live multi-project selection. Its evidence also remains
outside the repository.

The 2026-10-10 normal-chat regression preserves the production greeting's actual
classification split: general association 0.94, mode clarify 0.54/general 0.42.
Tests replay it with empty and populated catalogs and through the serial channel
worker while another project task waits. Additional cases cover project-name
and alias answers, multi-project answers, restart, duplicate callbacks and
repeated answers, multiple pending tasks, invalid directories and unrelated
conversation identity. These are automated regressions, not new live-DingTalk
acceptance. Historical waiting messages are retained and never auto-replayed.
`make check` and 439 routing/channel/state race cases passed for this fix.
The separate five-case live classifier probe (private validation evidence)
also passed: greetings/capability/email requests selected the default, while an
unowned login repair and unregistered repository required clarification. It used
the configured production Jev provider with synthetic project metadata; it did
not launch Claude or send DingTalk messages. Native and platform evidence below
predates this conversational fix.

Automated router cases cover exact names/aliases/repository references, purpose
matches, multi-project targets and primary selection, negation, substring
collisions, unknown projects, ambiguous engineering work, new topics and sourced
continuation. Invalid/failed Jev responses never fall through to execution.
The router race suite passed 132 cases on 2026-10-10, including the exact live
single-project, comparison, continuation and independent-general-work requests.

The state/channel race suite passed 273 tests. Integrated cases cover ordered
switch/choice/task handling, duplicate confirmations, unrelated messages while
waiting, restart, cancel, unavailable directories and changed configuration.
Joint execution exposes selected directories and scoped memories, keeps a
compatible set-specific native session and pauses new memory/experience publication.

| Evidence class | Current record |
| --- | --- |
| Automated checks | `make check` passed formatting, Go vet/suites, 20 installer tests and 27 viewer tests; state/channel race 273, router race 132 and assembly race 51 also passed. |
| Native Claude | Seven-turn multi-project report (private validation evidence) passed with controlled writes, independent native tests, both instruction formats, order-independent joint continuation, A → A+B → A separation and fresh-session recall from both scopes. |
| Live DingTalk | Verified private-chat report (private validation evidence) passed 17 isolated messages, eight native runs and one production-restoration command through the existing bot. It retains source delivery, seven future-observed key bot replies, reaction records and unchanged production evidence. |

The platform fixture verified automatic A/B and joint selection, a numbered
question surviving restart, unchanged original input plus a source-linked
restriction, duplicate confirmation without a new run, A → A+B → A continuation,
an ordered manual switch, joint `/new`, missing-directory failure before native
launch, independent general work and cancellation without execution. Its
confirmed task was constrained to read-only investigation; completed execution
does not establish a successful login repair. Native API retries occurred during
one turn, so this is functional acceptance rather than a latency or load result.

Final reaction records contain 46 `accepted`, 27 `skipped` and one `unknown`.
The restart-window send remains unknown locally even though the future listener
observed its reaction. It was not retried. Later execution/completion reactions
on the original clarified message were accepted and independently observed;
the final result used the confirmation message's reply capability. Cancellation
delivery was platform-accepted but its final notification was not independently
observed before the listener stopped. Reply, reaction and task outcomes remain
separate.

The report records the execution binary's SHA and the later `make install`
binary's SHA separately. Native-settings fingerprint hardening to policy v5
was validated with 51 assembly race cases; restoration installed that final
binary and verified a fresh Stream connection and a production `/project`
round trip. Original project, run, event, session, memory, revision and candidate
columns remained unchanged. The failed attempts remain alongside the successful
report in the [experiment guide](../experiments/runtime/README.md).

Use isolated project catalogs and memories for acceptance. Record failures as
well as successes. A successful model answer is not proof of native directory
access, platform delivery or business acceptance.
