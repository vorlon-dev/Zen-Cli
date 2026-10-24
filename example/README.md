# Example — integrating Zen OTA into an Android app

This folder shows the exact wiring for a host app. Two options:

| Option | What you copy | Best for |
|---|---|---|
| **Drop-in** | `ZenUpdater.kt` (1 file) | Zero build-system changes |
| **Library** | `zen-android` module | Multi-app / published AAR |

## 1. Application class

```kotlin
// App.kt
import ota.zen.android.ZenConfig
import ota.zen.android.ZenUpdater

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        ZenUpdater.init(
            this,
            ZenConfig(
                apiUrl  = "https://zen-ota-your-project.vercel.app", // no /api suffix
                appId   = "com.example.myapp",
                channel = "stable",          // stable | beta | alpha
                // installMode = InstallMode.AUTO,          // default: silent if device-owner, else prompt
                // autoCheck = true,                        // default: check on first Activity resume
                // checkIntervalMs = 4 * 60 * 60 * 1000L,   // default: max one auto-check per 4h
            )
        )
    }
}
```

## 2. Register in the manifest

```xml
<application
    android:name=".App"
    ... >

    <!-- Required for the OTA check + download (merged automatically if you use the library module) -->
    <uses-permission android:name="android.permission.INTERNET" />

    <!-- Required for installs on Android 8+ (prompt flow). Device-owner silent installs do not need this. -->
    <uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES" />
</application>
```

## 3. (Optional) react to update states in the UI

```kotlin
val stopListening = ZenUpdater.onStateChanged { state, detail ->
    when (state) {
        UpdateState.AVAILABLE   -> banner("Update ${detail ?: ""} available")
        UpdateState.DOWNLOADING -> progress(detail)               // detail = "42%"
        UpdateState.DOWNLOADED  -> toast("Update ready — installing")
        UpdateState.FAILED      -> toast("Update failed: $detail")
        else -> Unit
    }
}
// keep the returned function to stop listening later
```

## 4. What the user experiences

With a device-owner (silent) setup:

1. User opens the app → SDK checks `/api/check` in the background.
2. New version published (`zen publish app.apk`) → check responds with the release.
3. APK downloads silently (resumable, SHA-256 verified).
4. PackageInstaller session commits **without user action** → app process is killed.
5. User reopens the app → **they are now on the new version**. Nothing else.

"Open the app, close it, open it again — updated." Exactly the flow you wanted.

Without device-owner the SDK falls back to a one-tap system confirm dialog.

## 5. Enable silent installs (Device Owner)

One-time provisioning per device (device must have NO accounts configured):

```bash
# 5a. add the tiny receiver (see android/zen-android/README.md for the snippet)
adb shell dpm set-device-owner com.example.myapp/.ZenDeviceAdmin
```

Kiosk/MDM deployments usually already run as device owner — then zero code changes.

## 6. Publish an update

```bash
zen login                      # once per machine
zen init                       # once per project (creates zen.config.json)

# after every release build:
zen publish app/build/outputs/apk/release/app-release.apk \
    --channel stable \
    --notes "Bug fixes and performance improvements"

# test flight first
zen publish app.apk --channel beta --rollout 100 --notes "Beta build"

# gradual production rollout
zen publish app.apk --rollout 10 --notes "v2.0 — staged"
```

## 7. Verify from your laptop

```bash
curl "https://zen-ota-your-project.vercel.app/api/check?appId=com.example.myapp&versionCode=1&deviceId=test-device"
```

## 8. Flutter apps

Everything above applies unchanged — Flutter builds normal APKs, so the CLI and
API need nothing. Only the app-side integration differs:

```yaml
# pubspec.yaml
dependencies:
  zen_ota_flutter:
    git:
      url: https://github.com/YOUR_USERNAME/zen-ota.git
      path: flutter/zen_ota_flutter
```

```dart
// lib/main.dart
import 'package:flutter/material.dart';
import 'package:zen_ota_flutter/zen_ota_flutter.dart';

void main() {
  WidgetsFlutterBinding.ensureInitialized();
  ZenOta.init(const ZenConfig(
    apiUrl: 'https://zen-ota-your-project.vercel.app',
    appId: 'com.example.myapp',
  ));
  ZenOta.events.listen((e) => debugPrint('ZenOTA: ${e.state} ${e.detail ?? ''}'));
  runApp(const MyApp());
}
```

Publishing: `zen publish build/app/outputs/flutter-apk/app-release.apk --notes "..."`.
Full guide: `flutter/zen_ota_flutter/README.md`.
