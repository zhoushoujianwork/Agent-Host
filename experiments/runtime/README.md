# Live runtime assembly acceptance

The CLI fixture below is separate from the owner-authorized dws private-chat
check. Its successful platform report (private validation evidence) records
15 isolated source messages, six real Claude runs, independent reply/reaction
outcomes and a post-restoration production command round trip. The existing
application bot was used with separate synthetic projects, Host state and native
profile while production was stopped. No test project or memory was registered
in production. See the
[platform procedure and limits](../../docs/runtime-assembly-detail.md#live-dingtalk-acceptance-with-dws).
The initial attempt (private validation evidence) retains the richText
discovery and the runner's premature delivery-state check. Credential-bearing
configuration and complete databases remain in private local storage.

This controlled fixture uses two temporary registered projects, an isolated Host
database and one isolated native Claude profile. It verifies actual cwd, root
`AGENTS.md` compatibility, native `CLAUDE.md`, controlled file creation, separate
native continuations, fresh-session scoped recall and disabled auto memory.

Provider/model configuration is explicitly read from an installed runtime's
private configuration and passed only in child memory. No production database,
project files or DingTalk messages are used. Reports redact credentials and
temporary root paths. Failures are retained; the runner does not retry tasks or
change providers.

```sh
make build
python3 -B experiments/runtime/run.py --runtime-root ~/.agent-host/production --output experiments/runtime/results/live-2026-10-10.json
```

Use a fresh output filename on each attempt. This bounded synthetic experiment
does not establish platform acceptance or general memory quality.

## Recorded acceptance

- `results/live-2026-10-10.json`: the first native turn and memory save succeeded;
  the initial fixture then failed while iterating a null recall list. Evidence is retained.
- `results/live-2026-10-10-b.json`: all six turns passed after fixing that fixture
  and reading the native answer from the recorded result payload.
- `results/live-2026-10-10-final.json`: all six turns passed on the final binary,
  including the shared-settings fingerprint and session-rotation fixes. About 192 seconds.
- installation-2026-10-10.json (private validation evidence): fresh production
  Stream connection, matching binary, schema v6 → v7 and unchanged historical data.
  This record is separate from native execution reports.

Reports redact credentials and temporary business/extraction directory names,
retaining their distinct roles. To inspect them:

```sh
make viewer VIEWER_ARGS='--reports experiments/runtime/results --port 0'
```

The fixture projects were temporary and are not production registrations.
The earlier explicit-selection platform check passed as recorded above.

## Automatic session recovery

The recovery report (private validation evidence) records an
injected failed execution followed by two real, tool-free Claude calls using an
isolated profile: a new message automatically replaces the failed session, and
the following message continues the replacement. The original failure remains
unchanged. Channel replies and memory admission are fixtures; this check sends
no DingTalk messages and does not establish production platform acceptance.

```sh
AGENT_HOST_RECOVERY_LIVE=1 AGENT_HOST_RECOVERY_REPORT=/path/to/fresh-report.json go test ./internal/channel -run '^TestLiveNativeAfterAutomaticSessionRecovery$' -v -count=1
```

The test reads the installed runtime's private model credentials without copying
them into the report. The initial live fixture attempt rejected a noncanonical
temporary cwd before launch; the recorded run passed after correcting the fixture.
The earlier production streaming API 400 was not reproduced in isolated native
calls; automatic recovery does not claim to fix that upstream error.

## Multi-project native execution

`multi.py` extends native acceptance with two simultaneous project roots,
different root `CLAUDE.md`/`AGENTS.md` rules, controlled writes, independently
executed Python checks, argument-order-independent continuation, A → A+B → A
isolation and attributed recall from both existing project scopes. Joint runs
must retain evidence while skipping automatic memory and extraction.

```sh
make build
python3 -B experiments/runtime/multi.py --runtime-root ~/.agent-host/production --output /path/to/fresh-report.json
```

The verified seven-turn report (private validation evidence)
passed. Native test outputs are matched to successful Bash results by tool-use
ID; separately running the checks in the fixture verifies the resulting files.
The first attempt (private validation evidence) passed execution and
session checks but its mixed durable/transient alpha statement was not stored.
The second attempt (private validation evidence) stopped when the
fixture's declaration plus response request scored below the existing admission
threshold. The verified fixture uses plain single declarations. These failures
remain recorded; no provider or memory threshold was weakened.

`dws_multi.py` is a separately authorized platform acceptance runner. It uses
the verified owner profile and application-bot ID, a fresh private runtime and
future-event listener. It exercises inference, durable numbered clarification,
restart, duplicate confirmation, compatible sessions, joint reset, missing extra
directories, general work and cancellation. Production is stopped during the
isolated receiver and restored through `make install` afterward. Read each
report's `passed`, failure, restoration and observed-reply fields; send acceptance
alone does not establish final delivery. The initial
platform attempt (private validation evidence) found unnecessary clarification
when a clearly named project was accompanied by a memory restriction; production
was restored and the case became a routing regression test.
The second attempt (private validation evidence) passed automatic
single/joint routing and entered clarification, then stopped because its runner
used a byte log offset as a character offset. The retained log contains both
successful connections; production was restored. The runner now reads new log
bytes before decoding, and does not treat an old connection as a new one.
The third attempt (private validation evidence) verified restart,
source-preserving joint confirmation and duplicate confirmation. An explicit
return to alpha unnecessarily asked again when accompanied by a native-session
reference and a no-read constraint. Production was restored and this exact
request became a second router regression; thresholds remain unchanged.
The fourth attempt (private validation evidence) found that explicit
comparison of the two known projects still depended on semantic evidence scores,
causing a redundant question when one score fell below the threshold. This is
retained as a deterministic project-set routing regression. Production was
restored after every attempt.
The fifth attempt (private validation evidence) passed joint selection,
restart, confirmation, duplicate handling, A continuation, joint reset and
missing-directory failure. Independent general work was unnecessarily questioned:
the provider agreed on general work, but split its non-target probability between
`unrelated` and `excluded`. This became a categorical-consistency regression;
production was restored and the original evidence is retained.

The verified platform report (private validation evidence) passed:
17 isolated owner messages, eight native runs and one restored-production
`/project` command. Seven key bot replies were independently observed by the
future listener. Final reaction rows record 46 accepted, 27 skipped and one
unknown send across restart; later completion reactions were observed. The
unknown write was not retried. Cancel delivery was platform-accepted but was not
independently observed before listener shutdown. Production's original project,
run, event, session, memory, revision and candidate columns stayed unchanged.
One native turn included API retries; no Host task was replayed.

The report keeps execution and restored-installation binary SHAs separate.
Final policy-v5 fingerprint hardening was covered by 51 assembly race cases and
installed during restoration, with a fresh Stream connection and a real command
round trip. See the [routing acceptance notes](../../docs/smart-project-routing-detail.md#validation-and-acceptance)
for source, supplement, session and feedback limits.

Only run this platform fixture with explicit owner-message authorization and
a verified profile/bot identity, using fresh private state and output paths:

```sh
make build
python3 -B experiments/runtime/dws_multi.py --production ~/.agent-host/production --isolated-root ~/.agent-host/acceptance/FRESH_ID --profile VERIFIED_PROFILE --bot-id VERIFIED_BOT_ID --bot-name 'VERIFIED_BOT_NAME' --output /path/to/fresh-platform-report.json
```
