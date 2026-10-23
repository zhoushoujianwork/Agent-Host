# Platform Skills in the Web interface

Status: inspection and pinned authored-Skill export implemented on 2026-10-23.
The [managed Skills architecture](managed-skills-design.md) defines platform
ownership; [technical details](platform-skills-web-design-detail.md) describe
export, source coverage and validation.

## Goal and usage

**Agent-Host manages Skills; users inspect and export the results.** The platform
owns procedural learning, admission, versioning and effective selection. The Web
page should make these outcomes understandable without requiring users to import,
publish or assign each Skill as part of normal use. Existing CLI maintenance
commands remain available for deliberate source adoption and recovery.

Open Settings → Platform Skills, choose the configured runtime, and inspect a
Skill's purpose, versions, complete instructions and references. Select a version
and choose **Export version (ZIP)** to take a portable copy. Its manifest retains
source, review and lifecycle information. Export does not change the platform's
active version, grant tool permissions or synchronize edits back into Agent-Host.

For example, an SFTP Skill exports its common procedure together with separate
China and US references. Future tasks still resolve the actual region, account
and authorization afresh.

## Delivered scope

- The shared Settings page shows the complete authored registry for a selected
  configured Web/CLI or channel destination, including drafts and version history.
- A selected authored revision can be exported with all pinned files and a
  provenance manifest. The archive preserves the original content, including
  private material if present; on-screen previews retain credential masking.
- Agent → Capabilities shows the latest recorded effective catalog within 20
  recent associated runs, including learned experience, plugin guidance,
  dependency failures and observed loads. This is historical execution evidence.

Export currently covers the pinned authored registry. Learned-experience and
plugin exports require their own source adapters; historical descriptors alone
cannot reconstruct their full instructions. Automatic cross-procedure merging,
current cross-source readiness and production rollout remain separate work.

## Next steps

1. Extend inspection and export to platform-published experience, preserving its
   admission, audience and evidence boundaries; bind plugin exports to the owning
   provider's actual instruction/tool contract.
2. Consolidate related methods inside the platform using execution evidence:
   retain corrections, keep regional variants distinct, and produce a new version
   without erasing source evidence or bypassing admission.
3. Show why the effective version changed and whether the default Agent actually
   loaded it. Keep availability, observed loading and business acceptance distinct.

Manual registration/publish controls are no longer the next Web workflow.
A marketplace, general editor, generated-script execution and automatic public
publication are outside this scope.
