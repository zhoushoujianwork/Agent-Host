# Object retrieval native experiment

This controlled experiment exercises real statement admission and cross-session
recall through the built CLI, native Claude and company Jev endpoint. See the
[object retrieval guide](../../docs/object-retrieval.md) for current scope and
[details](../../docs/object-retrieval-detail.md#validation) for the fixed-suite
before/after comparison.

## Run

From the repository root, build the CLI and configure native credentials plus
`PATSNAP_API_KEY` securely. The default uses the process environment and normal
native provider settings:

```sh
make build
python3 experiments/retrieval/run.py --output /tmp/retrieval-observation.json
```

The existing explicitly authorized company `llm` configuration can instead be
read with `--credentials llm-alias --claude-config llm-alias`. The shared loader
reads supported values without executing shell aliases. It does not send the
company key to TypeSafe's official endpoint or substitute another provider.
Reports mask configured secrets and temporary root paths. Choose a new output
path for every observation; existing reports are preserved.

The script creates a temporary workspace and host database. Every turn uses a
fresh native configuration/session with hooks and native auto-memory disabled;
`--no-tools` disables tool execution. Three statements establish the order
service's PostgreSQL database, billing service's MySQL database and a 90-day
digital-key replacement interval. Two queries then check exact context IDs and
citations. The script never retries failed native work. It saves failures as
evidence and exits unsuccessfully when an assertion or operation fails.

## Retained observations

The first report (private validation evidence), recorded on 2026-10-10 with
Claude Code 2.1.223 and requested model `claude-opus-5-5`, passed five commands
and all 32 assertions. Jev admitted all three exact source statements. The
order-service query received only the PostgreSQL record. The broader credential
query received the 90-day key record; the native answer also distinguished this
key-specific statement from passwords, tokens and certificates.

Its binary fingerprint predates the final candidate tie-order correction and
stage-count wording. Each report retains its own fingerprint rather than being
rewritten to claim a different executable was tested.

The final-binary run (private validation evidence) repeats the five-turn
experiment after those changes and also passes all 32 assertions. Its binary
SHA-256 matches the final 26-case regression (private validation evidence).
Its native query turns took 15,452 ms and 14,159 ms, including model execution
and post-run processing. These are individual observations, not latency
percentiles or SQLite query timings.

After these successful runs, the script's diagnostic capture was hardened to
save command outcomes before parsing. Malformed JSON/NDJSON and inspection
timeouts now retain their raw output. Six synthetic checks covered malformed
chat/run/search output, a failed inspection, successful decoding and timeout,
including credential redaction. Those checks made no model calls; the retained
live reports keep their original report format and evidence.

This checks retrieval, memory persistence and context delivery. It does not
verify the real-world truth of the synthetic statements, measure general model
accuracy, or demonstrate causal task improvement or large-memory latency. The
16-case live Jev evaluation suite is separate and was not rerun by this script.
