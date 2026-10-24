# zen_ota_flutter

**Self-hosted in-app auto-updates for Flutter Android apps, backed by your own
GitHub repo.**

Publish a new APK with the Zen CLI and every user's app updates itself:

> open the app → update downloads in the background → silent install →
> **reopen — new version.**

No repo visits, no manual downloads, no uninstall/reinstall. Works for apps
distributed outside the Play Store (GitHub, direct download, MDM, kiosk).

Under the hood this plugin wraps the zero-dependency Kotlin SDK
(`ota.zen.android.ZenUpdater`) via platform channels — the exact same file used
by the native drop-in and AAR integrations, shipped verbatim inside the plugin.

## Requirements

- Android only (OTA APK installs are an Android concept; iOS App Store rules
  forbid third-party app self-updates)
- Flutter ≥ 3.3 / Dart ≥ 3.0
- A deployed Zen OTA API (see `api/` in the main repo) and the Zen CLI
  (`cli/`) for publishing

## Install

From your project root, referencing the plugin inside the zen-ota repo:

```yaml
dependencies:
  zen_ota_flutter:
    path: ../zen-ota/flutter/zen_ota_flutter
```

(or push the repo and use a git dependency:

```yaml
dependencies:
  zen_ota_flutter:
    git:
      url: https://github.com/YOUR_USERNAME/zen-ota.git
      path: flutter/zen_ota_flutter
```
)

Then add the install permission to `android/app/src/main/AndroidManifest.xml`:

```xml
<uses-permission android:name="android.permission.INTERNET" />
<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES" />
```

(`INTERNET` is merged from the plugin automatically; `REQUEST_INSTALL_PACKAGES`
must be added by the app for the one-tap prompt flow on Android 8+.)

## Quickstart

```dart
// lib/main.dart
import 'package:flutter/material.dart';
import 'package:zen_ota_flutter/zen_ota_flutter.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();

  // 1. Initialize — that's the whole integration.
  ZenOta.init(const ZenConfig(
    apiUrl: 'https://zen-ota-your-project.vercel.app', // no /api suffix
    appId: 'com.example.myapp',                        // your applicationId
    channel: 'stable',                                 // stable | beta | alpha
  ));

  // 2. (Optional) surface update progress in the UI.
  ZenOta.events.listen((e) {
    debugPrint('ZenOTA: ${e.state} ${e.detail ?? ''}');
    // e.state == ZenUpdateState.available && e.detail == 'forced'
    //   → show a NON-DISMISSABLE blocking dialog (contract).
    // e.state == ZenUpdateState.downloading
    //   → e.detail is a percentage string like '42%'.
  });

  runApp(const MyApp());
}
```

## What the user experiences

| Setup | Behavior |
|---|---|
| **Device-owner devices** (kiosk/MDM, or `adb shell dpm set-device-owner`) | Fully silent: app updates with zero taps; the process is replaced; next open is the new build |
| **Normal devices** | System install confirm appears when the APK is ready — one tap, done |
| **`updateRequired` release** | `AVAILABLE` arrives with `detail == 'forced'` — your UI must block usage until updated |

## Publishing an update

Build the APK as usual and publish it with the Zen CLI:

```bash
flutter build apk --release
zen publish build/app/outputs/flutter-apk/app-release.apk \
    --notes "Shiny new feature"
```

Split-per-ABI or appbundle outputs work too — publish the universal APK (or
build one per ABI as separate channels if you prefer; the manifest parser reads
the standard fields either way).

## API

### `ZenOta`

| Member | Description |
|---|---|
| `init(ZenConfig config)` | Initialize the native updater. Call once in `main()` before `runApp`. |
| `check()` | Force a check now (bypasses `checkIntervalMs`). |
| `events` | Broadcast `Stream<ZenUpdateEvent>` — listen early (in `main()`) to miss nothing. |
| `deviceId()` | Stable random UUID persisted per install (used for deterministic rollout bucketing). |
| `versionCode()` / `versionName()` | The currently installed APK's version, handy for about pages. |
| `initialized` | True after a successful `init` in this process. |

### `ZenUpdateState` reference

| State | Detail |
|---|---|
| `idle` | — |
| `checking` | — |
| `available` | `"forced"` when mandatory; `"waiting for an unmetered network"` when deferred |
| `downloading` | percentage string, e.g. `"42%"` |
| `downloaded` | versionName |
| `installing` | versionName (process is about to be replaced) |
| `up_to_date` | — |
| `failed` | human-readable reason |

## Silent installs (device owner)

One-time provisioning per device (device must have no accounts configured):

```bash
adb shell dpm set-device-owner com.example.myapp/.ZenDeviceAdmin
```

Kiosk/MDM fleets are usually already device owner — then this is a no-op and
updates are 100% silent. See the main repo's `android/zen-android/README.md`
for the 3-line `ZenDeviceAdminReceiver` snippet.

## Notes & gotchas

- **Hot restart** re-runs `main()` → `ZenOta.init` runs again; the native side
  is idempotent (config is replaced, listeners re-registered, no duplicate
  auto-check fires for the same process).
- **Hot reload** keeps the native state; use `ZenOta.check()` to re-check
  during development.
- The auto-check fires on the first Activity resume after process start;
  if `init` lands after that (rare timing), the plugin fires the launch check
  itself — either way, exactly one check per launch when `autoCheck: true`.
- Downloads resume across app restarts (`.part` files + `Range` requests) and
  are SHA-256 verified before install.
- On `installing`, the OS kills the process — persist any pending UI state
  before that if you care about it.
