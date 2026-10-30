# Read-only operations utilities

## Tencent CAM key inventory

`tencent_key_audit.py` inventories one explicitly named account using an existing
tccli credential profile. It uses the Tencent SDK rather than spawning concurrent
tccli processes. Select a Python interpreter with `tencentcloud-sdk-python`
installed; the script does not install dependencies or modify cloud resources.

```sh
python3 tools/operations/tencent_key_audit.py \
  --profile ACCOUNT_PROFILE --account EXPECTED_ACCOUNT_UIN \
  --output /private/path/to/one-audit-window
```

The inventory includes CAM users, active and inactive keys, group memberships,
group policies, user permission boundaries and all policies returned for each user. Key IDs are masked before
writing. Complete policy bodies and request IDs remain in private local evidence.
The SDK uses `ap-guangzhou` for the required STS region parameter; the CAM
inventory is account-wide.

Each completed user, group and policy is atomically checkpointed. Run the same
command to continue a stopped collection in the same audit window. Use a new
directory for a new audit; resumed evidence retains its individual timestamps.
Missing fields, inconsistent totals and incomplete group membership fail the
collection instead of silently producing a successful empty result.

An inventory is not a compliance verdict. Read the security team's baseline and
the effective policy statements, including conditions and exceptions. Root-account
keys and role/session credentials are not enumerated by the CAM user list and
must be checked separately when in scope. This utility does not exercise collected
keys or change their status, groups or permissions.

Run the offline regression checks with:

```sh
python3 -B tools/operations/tencent_key_audit_test.py
```

日常只读平台检查使用 `agent-host status --root /absolute/runtime --format json`，
无需直接查询 SQLite。检查口径和未知项见[平台状态](../../docs/platform-status.md)。
