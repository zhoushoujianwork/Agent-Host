# Channel execution permissions

Status: owner-only execution and approval requests are temporarily suspended
from 2026-10-19.

All channel users now receive `bypassPermissions` and the same native execution
tools as the Owner. Requests proceed without an @Owner approval step, including
read-only cloud CLI queries. Cloud-provider permissions still apply.

During Beta, all enabled datasources, mailboxes and configured DingTalk workspace
tools are shared across users and Agents. User/group source allowlists are paused.
Owner identity, private-user partitions and private Host history remain separate;
users are not relabelled as the Owner.
Before irreversible work, the assistant still confirms the exact scope and impact
with the requester unless the request already acknowledges them.

The existing restricted adapter and one-use approval implementation remain in
source, but ordinary channel requests do not activate them. Explicit
`chat --debug-non-owner` remains an opt-in local restricted-profile test.

See [implementation details and historical validation](dangerous-operations-design-detail.md).
