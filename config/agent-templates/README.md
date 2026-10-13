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
chmod 700 .agent-host/config/agents .agent-host/config/agents/default .agent-host/config/agents/ops
chmod 600 .agent-host/config/agents/default/*.md .agent-host/config/agents/ops/*.md
```

Use `default` and `ops` as the corresponding Agent IDs. Keep `default_agent`
set to `default` unless you intentionally select another registered Agent.
Copying files does not enable a role, grant tools, select projects or bind a
conversation. Templates contain no account settings or credentials.

Register the copied directories using `instructions_dir`, relative to the private
`agents.json` directory. The loader reads `AGENTS.md`, `SOUL.md` and `IDENTITY.md`
in that order; missing optional files are omitted. At least one must exist and
their combined content must fit 32 KiB. Directories must be private (0700), files
private (0600), valid UTF-8 and regular files without symlinks.

```json
{
  "default_agent": "default",
  "agents": [
    {"id": "default", "name": "Default assistant", "instructions_dir": "agents/default"},
    {"id": "ops", "name": "Operations assistant", "instructions_dir": "agents/ops"}
  ]
}
```

An optional `user_file: "users/owner/USER.md"` binds a manually maintained owner
profile of at most 16 KiB. This is explicit private configuration: no USER file
is created automatically, and group or meeting tasks do not receive it. The file
must use the same private-file rules. User profile data cannot grant capabilities.
Keep credentials out of role and profile documents.

The legacy absolute `instructions_file` remains supported for one file, but it
cannot be combined with `instructions_dir`. Configuration is loaded at startup;
restart after changing it. Profile and role changes participate in native session
compatibility. File-memory task copies live under `cache/workspaces/<run-id>/`,
with source hashes and scoped recalled knowledge in their manifest. The real
project cwd remains unchanged. See the [workspace design](../../docs/workspace-layout.md).

The Host does not update these private configuration files during learning.
Reusable lessons go to memory; task context files are derived under cache.
Template updates never overwrite user-maintained copies.
