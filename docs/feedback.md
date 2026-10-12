# Beta feedback

Report a runtime failure, missing memory, incorrect or unnecessary memory,
inappropriate memory recall, or a usability suggestion through the repository's
**Beta feedback** issue form. Describe what you expected, what happened and the
smallest reproduction you can share. See [report boundaries](feedback-detail.md).

Generate an optional local diagnostic summary:

```sh
./bin/agent-host feedback report --output /absolute/new/feedback.json
```

For a specific run, find its ID in the local viewer and add `--run RUN_ID`.
Managed DingTalk state needs its own home:

```sh
./bin/agent-host feedback report --home ~/.agent-host/production/state \
  --run RUN_ID --output /absolute/new/feedback.json
```

For a group or configured Agent, also use the run's `--agent ID` and
`--audience ID` from the local viewer. These select the existing partition;
the diagnostic report omits those identifiers. CLI options go before positional
arguments. The output parent must exist, and the file must be new.

Review the generated JSON before attaching or pasting it into an issue. It
contains build identity and structural status, timing and memory counts;
it does not include original messages, memory text or tool output. No report
is uploaded automatically. Keep the original run and evidence on your machine.
Use fictional examples when reproduction would expose private work.

For a memory case, say whether it helped, was irrelevant, was wrong, or did not
appear when expected. A memory being injected is not proof of usefulness.
