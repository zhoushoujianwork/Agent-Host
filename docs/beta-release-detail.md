# Independent beta: release notes

Companion to the [beta guide](beta-release.md). The completed daily-use increment
has its own [record](release-daily-use.md).

## Distribution

Run `make package VERSION=YOUR_VERSION` in the clean, reviewed development checkout to prepare
a local macOS archive from a clean Git commit. The version is explicit, with
embedded build revision and dirty
state, a manifest and archive SHA-256. Packaged files come from the public
allowlist; production credentials, state, logs and validation backups are excluded.
Publication and tags are separate from packaging. Development now continues
directly on the public repository's Git history; local development and GitHub
share the same commits. Follow the [development guide](development.md). The
legacy `tools/release/export_public.py` remains available for historical export
review, but normal updates no longer create a separate source snapshot.

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
