# Public viewer fixtures

These reports are hand-authored synthetic data. They contain no native/provider
output, platform identities, credentials, private paths or live conversations.
They test the saved-report projection; they are not execution or platform
acceptance evidence.

- `reviewed-experience.json` keeps the original completion receipt separate from
  later acceptance, an active injected revision and a later invalidation snapshot.
- `rejected-experience.json` records an ineligible experience with no publication.
- `attributed-retrieval.json` records three memories about two fictional services,
  attributed admission, exact-input annotations, recall exclusions and one memory
  whose source run is intentionally absent.

The retrieval report uses the inspected-native-command format, while the
experience reports use the explicit runs/commands format. This preserves coverage
of both supported archive versions without depending on private live reports.
