# User-managed Agent templates

These are copyable configuration examples, not an automatic Agent creation
mechanism. `default` provides a general assistant; `operations` provides an
operations assistant. Copy a template, edit its role, and register the Agent in
your private `agents.json`. Existing destination directories must not be
overwritten.

From the repository root, when the destination does not yet exist:

```sh
mkdir -p .agent-host/config/agents
cp -R config/agent-templates/default .agent-host/config/agents/default
cp -R config/agent-templates/operations .agent-host/config/agents/ops
```

Use `default` and `ops` as the corresponding Agent IDs. Keep `default_agent`
set to `default` unless you intentionally select another registered Agent.
Copying files does not enable a role, grant tools, select projects or bind a
conversation. Templates contain no account settings or credentials.

Current runtime supports an absolute `instructions_file` pointing to one file,
such as the copied `AGENTS.md`; it does not load `SOUL.md` or `IDENTITY.md`
automatically. Combined loading and template initialization are planned in the
[workspace design](../../docs/workspace-layout.md). Until that increment ships,
only the explicitly selected instruction file takes effect.

The Host does not update these private configuration files during learning.
Reusable lessons go to memory; task context files are derived under cache.
Template updates never overwrite user-maintained copies.
