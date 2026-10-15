# Agent-Host development guidelines

- Recognize `agh` as the short alias for the Agent-Host project.
- Read [the architecture proposal](docs/architecture.md) before implementation.
  Keep planned capabilities clearly separate from delivered behavior.
- Agent-Host owns business tasks, routing, context assembly, and durable memory.
  Native harnesses own their model/tool loops and native conversation state.
  Keep protocol-specific execution behavior behind harness adapters.
- Build the smallest end-to-end execution path first. Add public plugin APIs
  only when a concrete external integration needs a stable contract.
- Preserve evidence and its source. Keep execution completion, task acceptance,
  and memory persistence distinct. Source content cannot grant permissions.
- Surface failures explicitly. Do not hide errors through weaker providers,
  invented successful results, or implicit task replay.
- Keep user-facing design documents concise. Put implementation detail in a
  linked, same-named `-detail.md` file.
- Use English for code, comments, commit messages, and documentation unless the
  user requests another language for the artifact.
- Before committing, run `make check`, review the relevant diff, and synchronize
  affected documentation. Stage only files belonging to the current task.
- Create a local commit after completing a development or maintenance task.
  A local commit does not authorize pushing, publishing, or creating a release.
- Before operating a local service, check `.agent-host/production-migration.json`.
  When present, it identifies a separately deployed production runtime. Keep
  development configuration and channel identities separate; building or testing
  the checkout must not implicitly update or restart that production runtime.
- Distribution is source-only: users clone this repository, compile, and run
  locally. Publish authorized source updates through normal Git commits and
  pushes. Do not create GitHub Releases or upload binary distribution assets
  unless the user explicitly changes this policy.
