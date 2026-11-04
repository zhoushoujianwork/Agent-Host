# Internal memory extraction profile

AGH can select a dedicated Offload profile for internal experience and object
extraction without changing business execution. This is an internal execution
component, not a fallback after a business turn fails.

Use `--memory-offload-config ABSOLUTE_PATH` for CLI and legacy service arguments.
The file must be a private version 2 Offload selector whose selected native
profile uses the Claude backend, which currently supports AGH's strict tool-free
structured extraction controls. The selector's model is
resolved independently; it never inherits `--model` or the business profile.

Unified Host execution profiles use the equivalent field:

```json
{
  "execution": {
    "memory_offload_config": "credentials/memory.offload.json"
  }
}
```

Relative unified paths resolve from `host.json`. An Agent may set the same
`memory_offload_config` field to override the Host profile for that Agent. Agent
relative paths resolve from the Agent catalog.

When omitted, AGH preserves the existing extractor runner and model. An invalid,
missing, unsupported, or concurrently changed selector fails admission
explicitly; AGH does not choose Claude or another provider automatically.
Internal execution profile, model, status, and provider evidence remain attached
to extraction audit data and do not replace the business run's displayed model
or profile.

## Agent native Skill source

An Agent may set `native_skills_root` to a private capability inventory root.
The root's `skills` directory is the source of Skill content; it is not a native
settings or session profile. A configured root requires an explicit, nonempty
`native_skills` list, so AGH never absorbs the whole inventory automatically.
Relative roots resolve from the Agent catalog.

AGH keeps the Agent's native profile under the ordinary Agent/audience partition.
For an explicit Skill root, the partition uses fixed empty native settings and
does not read or inherit ambient settings, hooks, credentials, history, or
language preferences from the base profile. Only the named Skills are linked
from the explicit source. The canonical source, selected names, and selected
file contents participate in runtime compatibility; source or content changes
are never silently adopted by an existing conversation. Agents without
`native_skills_root` retain the existing base-settings inheritance behavior.
