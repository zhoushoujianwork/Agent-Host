# Historical CLI memory validation

This document summarizes private historical validation. Its gateway
runner and raw reports are omitted from the public source export.
Use the public CLI and synthetic evaluation fixtures for local checks;
the observations below do not establish general accuracy.

## Recorded observations

- `results/patsnap-live-2026-10-10.json` and
  `results/patsnap-live-repeat-2026-10-10.json`: retained failures under the
  initial policy and native settings configuration (`cc-proxy`, Sonnet 5).
  Native turns were protocol-successful but returned unrelated text. The first
  memory was not stored. The repeated case records category probabilities:
  constraint 0.72, project fact 0.20, decision 0.07, none 0.01. Using a category
  threshold as an eligibility gate caused an avoidable abstention.
- `results/patsnap-alias-v2-2026-10-10.json`: four cases passed with explicit
  `llm` native configuration (Opus 5.5) and the v2 decision policy. Eligibility
  and category are independently judged in one request; the eligibility
  threshold remains 0.8. The second turn cited the injected memory ID. The
  fourth turn had no active recalled memory and answered that it did not know.
  This run preceded the final explicit SQLite persistence phase and receipt
  wording changes; it is retained as an intermediate observation.
- `results/patsnap-final-2026-10-10.json`: the final binary passed all four cases,
  including separate recall/execute/remember/persist timings and an explicit
  quarantine receipt. Native Claude Code 2.1.223 used `claude-opus-5-5`; the
  gateway returned `typesafe/jev-1.13-20260917`. Total turn times were 20.38–25.29 s;
  Jev phases were 0.86–1.91 s. These are observations, not service guarantees.

The validation report includes received event sequences, time to first delta,
overall latency, actual model names, saved/recalled records and typed Jev
answers. Current runner output also records the native version, binary hash,
and per-phase durations. Temporary native/SQLite data is removed after the run;
the checked-in report is the retained evidence. Tests cover durable journal
replay independently with temporary real SQLite databases.

The policy revision and explicit native configuration change are both recorded.
The successful rerun does not isolate the effect of model choice from every
other condition, nor establish overall Chinese accuracy. See the
[MVP design](../../docs/mvp-validation.md) and
[implementation/validation details](../../docs/mvp-validation-detail.md).
