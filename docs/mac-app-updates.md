# Mac app updates

Open **App Preferences** to see the installed version, stable/dev channel, last
check and update status. **Check for Updates…** is also in the native app menu.
[Implementation and recovery](mac-app-updates-detail.md).

- **Stable:** checks in the background, builds an eligible stable version locally
  and applies it when Workspace is safely idle and the app is in the background.
  **Later** defers it for 24 hours.
- **Dev:** reports an available revision; **Update** is required before building
  or applying it. **Later** leaves the current app in place. A dev update must
  include the installed revision, so older or divergent sources are refused.
- **Local changes:** modified builds are kept in place. Rebuild from a clean
  commit before using the updater.

Updates keep the chosen Host connection, unsent draft and inbox state. The
installer verifies its candidate before the app exits, retains the previous app
and rolls back if the new app fails its startup health check. Separately deployed
Host/channel services are not replaced or restarted.
After a failed installation, automatic attempts pause until **Check for Updates**
is selected. The current app remains open if installer preflight fails.

Distribution remains source-only: updates build from the official repository and
require the local build tools. Stable publications use exact `vMAJOR.MINOR.PATCH`
source tags; without one, the app reports that no stable version is published.
No binary release or stable tag is created by this implementation.

Headless update and recovery checks use isolated fixtures. A real published-source
replacement/relaunch remains pending acceptance; passing those tests is not proof
of a successful production upgrade.
