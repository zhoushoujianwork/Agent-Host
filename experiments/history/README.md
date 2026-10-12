# History and topic-discovery acceptance

These opt-in checks distinguish synthetic native fixtures, real saved receipts,
and selected live DWS data. Reports contain structural checks only; source text,
identities, native outputs, credentials and private paths stay outside the
repository. Normal `make check` runs only offline tests.

## Synthetic native checks

These use real Claude with isolated synthetic SQLite/source fixtures. They never
read a live DWS account or send a DingTalk message.

Build the Host, then run with the configured native executable on `PATH`:

```sh
make build
AGENT_HOST_LIVE_HISTORY=1 AGENT_HOST_LIVE_REPORT=/absolute/path/history.json \
  go test ./internal/cli -run '^TestNativeConversationHistoryLive$' -count=1 -v
AGENT_HOST_LIVE_DISCOVERY=1 AGENT_HOST_LIVE_REPORT=/absolute/path/discovery.json \
  go test ./internal/discovery -run '^TestNativeDiscoveryLive$' -count=1 -v
```

History additionally supports `AGENT_HOST_LIVE_CLAUDE` and
`AGENT_HOST_LIVE_BINARY`; `AGENT_HOST_LIVE_MODEL` selects an explicit native
model for either check. Normal `make check` skips all live invocations.

The history report (private validation evidence) verifies actual
search/read tool pairs, saved formatted reply and delivery-state recovery, and
refreshed receipt access in the same resumed native session.
The discovery report (private validation evidence) verifies cited
topic extraction, pre-acceptance evidence review, project-bound private export,
no task/memory side effects and unchanged-overlap deduplication.
The installation report (private validation evidence) records a fresh
production Stream connection, backed-up schema upgrade and preservation of all
legacy table rows; DWS collection remained disabled.

## Real selected-source discovery

[dws_discovery.py](dws_discovery.py) uses the already verified private
`context.json`, `discovery.json`, isolated project/state, native profile and
initial collection proof. It checks the same selected DM under its fixed DWS
profile, reviews its topic, accepts citations in private test state, exercises
real message limits and completes the original pending range after raising them.
It sends no messages and does not change production state.

```sh
rtk python3 -B experiments/history/dws_discovery.py \
  --private-root /absolute/private/acceptance \
  --production /absolute/private/production \
  --binary /absolute/path/agent-host --output /absolute/structural-report.json
```

The real discovery report (private validation evidence) records four
messages, one native topic with four original citations, zero overlap additions,
and a capped three-message range completed with exactly one new evidence revision.
No tasks, messages or memory were created by discovery.

## Genuine saved-receipt native check

Use a SQLite online backup of actual admitted receipts. Prepare a private native
profile at `home/native-profile`, disable hooks and automatic native memory, and
provide a private JSON configuration with `home`, `current_receipt`,
`expected_receipt`, `native_executable`, `host_executable` and `model`. All paths
are absolute. Independently correlate the expected saved reply through the
selected DWS conversation; do not manufacture a receipt to fill a gap.
Optional `knowledge_config` and `knowledge_sources` mount the selected knowledge
MCPs alongside history, using only the current receipt's project and audience
grants. The check invokes only history tools and reports mount counts; it uses
the production permission mode with a separate prepared native profile.

```sh
AGENT_HOST_LIVE_HISTORY_CONFIG=/absolute/private/config.json \
AGENT_HOST_LIVE_REPORT=/absolute/structural-report.json \
  rtk go test ./internal/cli -run '^TestNativeConversationHistoryDWSDataLive$' -count=1 -v
```

The genuine-receipt native report (private validation evidence)
records actual search/read results, exact full saved reply, author and delivery
state, excluded current request, and an unchanged copied database. This test
itself invokes neither DWS nor delivery.

The production streaming diagnosis (private validation evidence)
retains a later failed owner request: all five MCPs connected and history search
succeeded, then the model API returned a streaming-required 400. Copied-production
checks with the same model and five selected MCPs subsequently failed with
upstream temporary-unavailability and an empty stream. The adapter now disables
mid-stream non-streaming fallback and retains native diagnostics on nonzero exit.
Offline checks and service reconnection passed; these mitigations do not establish
restored production conversation success. No new platform message or failed task
was replayed. Earlier passing isolated reports remain bounded evidence.

The later processing recovery validation (private validation evidence)
retains three-way gateway evidence: an injected empty stream fails with fallback
disabled, a 64,000-token native fallback receives the same streaming-required
400, and an explicitly bounded 8,192-token fallback succeeds. The updated adapter
completed through the same provider/model after one successful Read tool call;
the native profile could not override its invocation limit. Production opted in
to this bounded native recovery. Local official-SDK WebSocket tests also verify
disconnect/reconnect without canceling Host work; the installed service restarts
unexpected exits with a 30-second throttle. `make check`, 493 affected race cases,
installation and a fresh production connection passed. All 9,151 historical rows
in 28 tables remained unchanged. No new DingTalk task round trip is claimed, and
the historical failed tasks were not replayed.

The subsequent authorized cc gateway switch (private validation evidence)
supersedes that production token workaround. The daemon now uses the owner's
`cc` gateway/credential and exact `claude-sonnet-5[1m]` model, with no Host output
override or 8192 ceiling. Its updated adapter passed same-model empty-stream
recovery with the native 64,000-token budget and one successful Read. Production
reconnected after installation, preserving all 9,244 historical rows; a new
DingTalk task round trip remains separate and unverified.

## Four explicitly approved platform turns

[dws_platform.py](dws_platform.py) requires direct approval of the existing bot
target, four messages and temporary receiver switching before using
`--send-authorized`. The sequence saves a marker reply, sends `/new`, recalls
through both tools, restarts the isolated service and recalls again. Private
`context.json` and verified `conversation-info.json` select the exact owner DM.
The runner records genuine send IDs, actual paired tool results, platform reply
details and production restoration. It never replays an unknown send or task.

```sh
rtk python3 -B experiments/history/dws_platform.py \
  --production /absolute/private/production \
  --private-root /absolute/private/acceptance \
  --binary /absolute/path/agent-host --output /absolute/structural-report.json \
  --send-authorized
```

The original platform report (private validation evidence) records an
interrupted sequence: the third send was accepted, but its immediate status read
returned the observed `PARAM_ERROR / 消息不存在` error. A later read of the same
genuine task confirmed success; the message has no Host receipt from the receiver
gap and was not replayed. The runner now retains the receiver while retrying only
that exact observed read error for at most 45 seconds. Other errors fail explicitly.
Offline regressions also cover production tables declared `WITHOUT ROWID`.

The remaining approved fourth-turn report (private validation evidence)
records the passing bounded follow-up separately: actual search/read recovered
the original reply and delivery state after `/new` and restart. DWS verified all
four source sends and reread the three completed bot replies by genuine IDs.
Production reconnected with all original rows preserved. It cannot establish
compatible native continuation across a live restart because the third Host turn
never ran. The synthetic native continuation report remains separate evidence.

Sustained polling, group topic quality and account-wide coverage remain unverified.
The earlier [bounded DWS retrieval experiment](../dws/README.md) is separate.
See the [feature guide](../../docs/conversation-history.md) and its
[implementation details](../../docs/conversation-history-detail.md).
