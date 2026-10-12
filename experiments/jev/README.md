# Jev synthetic evaluation

This Python standard-library experiment is separate from the Go host runtime.
It sends only synthetic fixture content to the Patsnap endpoint explicitly
selected by the user. Read the [report](../../docs/jev-evaluation.md) and
[method](../../docs/jev-evaluation-detail.md) before interpreting the scores.

Validate fixtures without credentials or network access:

```sh
python3 -B experiments/jev/run.py --validate-only --output /tmp/jev-unused.json
```

Run all 40 main cases using `PATSNAP_API_KEY` from the environment. Choose a new
output filename; the runner refuses to overwrite a prior run. This makes paid
remote API calls. The explicit encoding flag is required by the tested gateway.

```sh
python3 -B experiments/jev/run.py --credentials env --string-instructions \
  --output /tmp/jev-new-main.json
```

On the originating workstation, `--credentials llm-alias` can read the literal
credential assignment in the user's `.zshrc` alias after a hostname check. It
does not execute the alias or save the credential. Never put a key in a command,
fixture, result, or Git commit. The saved artifacts contain no authorization
headers.

Use repeated `--case <id>` arguments to select cases. `--suite diagnostic`
runs the three explicitly post-hoc rubric variants; do not add their results
to the main score. A failed request stops the runner and retains all observations.
No silent retries, cached replacements, or provider switching are performed.

The gateway alias resolved to the same dated model ID in all recorded responses.
Future alias results may change; inspect the returned model before comparisons.
Scoring thresholds are experimental and are not production calibration.
