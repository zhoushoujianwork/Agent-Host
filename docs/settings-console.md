# Settings console

Manage the local Host configuration from the workbench instead of locating and
editing several private files. OpenFilm's settings console informs the layout:
searchable categories on the left, grouped controls and status on the right.
See [implementation details](settings-console-detail.md).

## Usage and scope

Open **Settings** from the execution workbench. Select Agents, models/providers,
channels, data sources, backups, people or CLI handoffs. Edit the relevant fields,
review the changed configuration and save. Agent instruction files are editable
beside their configuration. Nested controls support the existing configuration
fields without inventing a second configuration format.

The redesigned page also provides light, dark and system appearance under
**外观与主题**. The selection applies immediately across the
[Web workspace](web-workspace.md) and stays in the browser; it is separate from
Host configuration and the Save changes action.

The console edits saved local configuration. Provider passwords/tokens stay hidden
on load; leaving a secret unchanged retains its existing value. Replacing or
clearing it is explicit. Native account sign-in remains in the native tool.

Saves preserve a private previous version, reject conflicting edits and run the
existing Host service configuration check. A failed check restores the prior
file. Saving never restarts channels or executes a business task. The page tells
the user which changes require a service restart; CLI handoffs read their saved
settings on the next invocation.
These are local configuration checks; provider sign-in and connectivity are
separate from saving settings.

## Delivery and validation

The local API and grouped editor are implemented. Automated checks cover secret
preservation, conflicting writes, invalid-save rollback and HTTP boundaries.
Browser verification saved an Agent name and provider endpoint in an isolated
fixture, retained its credentials and checked the mobile layout. The real Host
was checked locally and its configuration was displayed without browser edits.

The existing execution and memory workspaces remain available. Remote
administration, a replacement authentication system and new execution adapters
are outside this change.
