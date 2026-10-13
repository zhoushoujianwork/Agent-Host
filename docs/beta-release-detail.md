# Source distribution notes

Companion to the [source setup guide](beta-release.md). The completed daily-use increment
has its own [record](release-daily-use.md).

## Distribution

Users clone the public repository and compile locally with `make build`.
Development and public updates share the same Git commits; normal publication
pushes source changes without GitHub Releases, binary packages or a separate
public-source snapshot. Follow the [development guide](development.md).

The source includes configuration examples, foreground-runner/viewer tools, guides,
LICENSE and dependency notices. Users supply their own native harness and
authentication. This is not a bundled model or shared hosted service. Building
requires Go 1.26+ and Make; channel startup is handled by the Go CLI, and the
read-only viewer needs Python 3.10+.
The historical packaging/export utilities remain for compatibility and audit,
not as the current distribution workflow.

## Evidence and limitations

Checks cover source compilation, build identity, read-only feedback projection
and private output files. Historical archive/checksum tests do not imply current
binary distribution. Production continuity, public
footers and controlled interruption recovery have separate retained evidence.
A second real non-owner group account and sustained independent tester use
remain observations to collect; source publication does not establish them.

Earlier daily-observation records are retained as private historical evidence;
the current project-local runtime does not imply a scheduled observer. Colleagues
explicitly submit reports; no central telemetry service or automatic upload is
part of this beta.
