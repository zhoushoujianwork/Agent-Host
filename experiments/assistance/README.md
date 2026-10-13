# Scoped memory assistance acceptance

`make check` covers stable matter identity/progress, source withdrawal before
model input, scope/lifecycle checks, feedback, read-only tools and atomic
preparation selection without replay. State, assistance, Host, discovery and
history also run under the Go race detector.

The 2026-10-12 structural reports separate two real-model checks:

- Native loop (private validation evidence): synthetic conversations and
  seeded exact user-statement memory; English and Chinese fresh native sessions
  with no prefetched facts or supplied record IDs. Paired tool results recovered
  a random source-only readiness marker and original citations. Preparation
  completed using saved guidance, repeat selection was blocked, and answers/
  requester cancellation updated stable matters and suppressed stale choices.
  Seeded retrieval does not measure memory admission accuracy.
- Selected DWS pilot (private validation evidence): one previously selected
  authenticated DM, 12 retained real messages, four cited matters and fresh
  native retrieval. Final review found two reported resolutions, one cancelled
  request and one conflicting report. Zero suggestions remained; cancelled
  work was not proposed for retry. No message or discovery-derived memory was
  published.

An earlier 48-hour/99-message probe collected complete evidence but timed out
in native analysis at 180 seconds. It remains a failed probe. The smaller
three-hour pilot and bounded reconciliation passed; wide-window performance,
account-wide recall, upstream deletion coverage and long-term usefulness remain
unmeasured. Production configuration and source grants were not changed.

Raw messages, copied state, model answers and provider details stay in private
acceptance storage outside Git. Checked-in reports contain structural evidence.

## Reproduce

Build `bin/agent-host`. Set `AGENT_HOST_LIVE_ASSISTANCE=1`,
`AGENT_HOST_LIVE_HOST` to that absolute host executable,
`AGENT_HOST_LIVE_CLAUDE` to an authenticated native executable, and optionally
`AGENT_HOST_LIVE_MODEL`. Then run:

```sh
go test ./internal/cli -run '^TestNativeAssistanceRetrievalAndPreparationLive$' -count=1 -timeout=15m
```

Preparation uses existing Jev credentials: `TYPESAFE_API_KEY`, or `JEV_API_KEY`
with explicit `AGENT_HOST_LIVE_JEV_ENDPOINT` and optional
`AGENT_HOST_LIVE_JEV_MODEL` for an already configured custom provider. No
fallback is introduced. Keep credentials off command lines.

The real-source test needs `AGENT_HOST_ASSISTANCE_DWS_CONFIG` (an owner-private
config with exactly one already selected source), `AGENT_HOST_LIVE_PRIVATE_ROOT`
(an absolute private directory), and the same native/host executable settings.
Optional `AGENT_HOST_DWS_PILOT_HOME` names isolated reusable pilot state. Run
`TestSelectedDWSAssistancePilotLive` explicitly. It reads the configured source;
it neither selects another conversation nor sends a message.
