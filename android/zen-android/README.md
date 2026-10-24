# zen-android — Zen OTA Android library module

Gradle Android **library module** wrapping `ZenUpdater.kt` (a byte-identical
copy of `../dropin/ZenUpdater.kt`), publishable as an AAR.

- Package: `ota.zen.android`
- `compileSdk` 34, `minSdk` 21, Kotlin `jvmTarget` 17
- **Zero runtime dependencies** — only `android.*`, `org.json` (part of the
  Android SDK), the JDK and the Kotlin standard library. No support libraries,
  no HTTP client, no coroutines.

## 1. Include the module

Drop the `zen-android/` folder into your project, then in your **root**
`settings.gradle.kts`:

```kotlin
include(":zen-android")
```

and in your **root** `build.gradle.kts` make sure the plugins are on the
classpath (this module declares the plugin ids without versions):

```kotlin
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
}
```

In your **app** module:

```kotlin
dependencies {
    implementation(project(":zen-android"))
}
```

## 2. Permissions (app manifest)

```xml
<!-- mandatory -->
<uses-permission android:name="android.permission.INTERNET" />

<!-- recommended: needed for the user-confirm install flow on API 26+ -->
<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES" />

<!-- optional: only if ZenConfig.showProgressNotification = true (API 33+) -->
<uses-permission android:name="android.permission.POST_NOTIFICATIONS" />
```

The library manifest already merges `INTERNET`; declare the other two yourself.

## 3. Quickstart

```kotlin
// App.kt
import ota.zen.android.ZenConfig
import ota.zen.android.ZenUpdater
import ota.zen.android.UpdateState

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        ZenUpdater.init(
            this,
            ZenConfig(
                apiUrl = "https://your-zen-ota.vercel.app", // no /api suffix
                appId  = "com.example.app",                 // your applicationId
                channel = "stable",                          // stable | beta | alpha
                installMode = InstallMode.AUTO               // silent if Device Owner, else prompt
            )
        )
    }
}
```

```kotlin
// MainActivity.kt
override fun onResume() {
    super.onResume()
    dispose = ZenUpdater.onStateChanged { state, detail ->
        when (state) {
            UpdateState.AVAILABLE -> if (detail == "forced") showBlockingUpdateDialog()
            UpdateState.DOWNLOADING -> progressBar.progress = detail?.trimEnd('%')?.toInt() ?: 0
            UpdateState.FAILED -> Log.e("App", "OTA failed: $detail")
            else -> Unit
        }
    }
}

override fun onPause() { dispose?.invoke(); super.onPause() }
```

Manual check: `ZenUpdater.check(activity)` (bypasses the 4h interval).

## 4. Device Owner silent installs

Fully silent, zero-UI installs work when the app is a **Device Owner**
(kiosks, managed devices, custom ROM builds):

```bash
adb shell dpm set-device-owner com.example.app/.ZenDeviceAdmin
```

`dpm set-device-owner` requires a `DeviceAdminReceiver` to exist. The minimal
implementation is 3 lines of code plus a small manifest/resource snippet:

```kotlin
// ZenDeviceAdminReceiver.kt
class ZenDeviceAdminReceiver : android.app.admin.DeviceAdminReceiver()
```

```xml
<!-- AndroidManifest.xml (inside <application>) -->
<receiver
    android:name=".ZenDeviceAdminReceiver"
    android:exported="true"
    android:permission="android.permission.BIND_DEVICE_ADMIN">
    <meta-data
        android:name="android.app.device_admin"
        android:resource="@xml/zen_device_admin" />
    <intent-filter>
        <action android:name="android.app.action.DEVICE_ADMIN_ENABLED" />
    </intent-filter>
</receiver>
```

```xml
<!-- res/xml/zen_device_admin.xml -->
<device-admin xmlns:android="http://schemas.android.com/apk/res/android">
    <uses-policies />
</device-admin>
```

Run the adb command **before** signing into a Google account on a fresh
device/emulator (`adb shell dpm list owners` to verify). On API 31+ the SDK
sets `setRequireUserAction(false)` on the install session; on API 21–30
Device-Owner sessions commit silently by default. After commit the system
kills the process and the app reopens on the new version.

Non-owner devices automatically fall back to the PackageInstaller
user-confirmation dialog (API 24+) or a legacy install intent (API 21–23).

## 5. Behavior notes

- **Checks**: automatic on first Activity resume (respects `checkIntervalMs`,
  default 4 h, persisted across restarts) or manual `check()`.
- **Downloads**: resumable (`Range` header) into
  `Android/data/<pkg>/files/zen-ota/<versionCode>.apk.part`, verified with
  SHA-256, then renamed to `<versionCode>.apk`. Interrupted downloads resume
  on the next launch.
- **Install chain**: silent (Device Owner) → PackageInstaller prompt (API 24+)
  → legacy intent (API 21–23). On API 26+ the prompt requires
  `REQUEST_INSTALL_PACKAGES`; the SDK emits `FAILED` with the reason
  `REQUEST_INSTALL_PACKAGES permission missing` when absent.
- **Events**: after the update, the next launch posts `install_success` to
  `POST /api/event` (reconciliation via the persisted pending-install record);
  install exceptions post `install_failed`.
- **Forced updates** (`minVersionCode` below the running version): the SDK
  emits `AVAILABLE` with detail `"forced"` and downloads regardless of
  metered-network preferences — your UI must show a non-dismissable dialog.

## 6. Publishing (placeholder)

`build.gradle.kts` already contains a `maven-publish` block producing the
`release` AAR publication (`app.zen.ota:zen-android:0.1.0`). Add your
repository (Maven Central, JitPack, private Nexus…) in the consuming project
and run `./gradlew :zen-android:publish`.

## 7. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `check failed: HTTP 404 (unknown channel)` | Channel typo or channel never published (`zen publish` first). |
| `silent install requires device-owner privileges` | Device is not provisioned — see §4, or switch to `InstallMode.PROMPT_ONLY`. |
| `REQUEST_INSTALL_PACKAGES permission missing` | Declare the permission and let the user grant "install unknown apps". |
| `download failed: checksum mismatch` | APK changed after publishing; re-publish with `zen publish`. |
| No progress notification on API 33+ | Grant `POST_NOTIFICATIONS`, or set `showProgressNotification = true` in `ZenConfig`. |
| Silent install committed but app never reopens | Normal: the OS restarts the updated app process; verify with `adb shell dumpsys package com.example.app | grep versionCode`. |
| Download restarts instead of resuming | Server did not honor the `Range` header (the SDK detects this and restarts cleanly). |
