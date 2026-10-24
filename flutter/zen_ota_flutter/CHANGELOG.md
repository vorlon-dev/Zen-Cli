## 1.0.0

- Initial release.
- Dart API: `ZenOta.init / check / events / deviceId / versionCode / versionName`,
  `ZenConfig`, `ZenInstallMode`, `ZenUpdateState`.
- Native bridge (Kotlin) over the verbatim zero-dependency `ZenUpdater.kt`.
- Silent installs on device-owner devices, one-tap confirm otherwise.
- Resumable, SHA-256-verified background downloads.
- Channels (stable/beta/alpha), staged rollout, forced updates — powered by the
  Zen OTA API + Zen CLI.
