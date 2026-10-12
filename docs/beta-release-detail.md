# Independent beta: release notes

Companion to the [beta guide](beta-release.md). The completed daily-use increment
has its own [record](release-daily-use.md).

## Distribution

Run `make package VERSION=0.2.0-beta.1` in the reviewed public export to prepare
a local macOS archive from a clean Git commit. The version is explicit, with
embedded build revision and dirty
state, a manifest and archive SHA-256. Packaged files come from the public
allowlist; production credentials, state, logs and validation backups are excluded.
Publication and tags are separate from packaging. `tools/release/export_public.py`
prepares a new source snapshot without private validation reports or original
Git history; review and commit that snapshot before packaging or publication.

The archive contains the native binary, configuration examples, installation
and viewer tools, user guides, LICENSE and dependency notices. It requires the
tester's own native harness and authentication. It is not a bundled model or a
shared hosted service. Source users also need Go 1.26+ and Make; packaged users
need Python 3 for managed installation and the viewer.

## Evidence and limitations

Checks cover build identity, read-only feedback projection, private output files,
archive contents and checksum verification. Production continuity, public
footers and controlled interruption recovery have separate retained evidence.
A second real non-owner group account and sustained independent tester use
remain observations to collect; packaging does not establish them.

Local daily observation runs at 17:00 Asia/Singapore and records under
`~/.agent-host/observations/daily/`. It observes this owner's production instance,
not colleagues' machines. Colleagues explicitly submit reports; no central
telemetry service or automatic upload is part of this beta.
