# Zen OTA — Android SDK

In-app auto-update for Android against a Zen OTA API deployment
(`https://<deployment>/api`). Two integration options, **same engine**:

| Option | What you ship | Best for |
|---|---|---|
| **A. Drop-in** | copy `dropin/ZenUpdater.kt` into your sources | zero build-system changes, single file |
| **B. Library** | include `zen-android/` as a Gradle module | teams, versioned dependency, future AAR publication |

Both expose the identical API (`ZenUpdater`, `ZenConfig`, `InstallMode`,
`UpdateState`) and have **zero external dependencies** — only the Android
framework (`android.*`, `org.json`), the JDK and the Kotlin standard library.
The two `ZenUpdater.kt` copies are kept byte-identical on purpose.

---

## Requirements

- `minSdk` 21, `compileSdk` 34, Kotlin 1.9+, AGP 8.x
- A Zen OTA deployment with your app published (`zen publish`)

---

## Option A — Drop-in (copy 1 file)

1. Copy `dropin/ZenUpdater.kt` into your app source tree (package
   `ota.zen.android`, or change the package line to fit your project).
2. Add permissions to `AndroidManifest.xml`:

```xml
<!-- mandatory -->
<uses-permission android:name="android.permission.INTERNET" />

<!-- recommended: user-confirmation installs on API 26+ -->
<uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES" />

<!-- optional: only if showProgressNotification = true (API 33+) -->
<uses-permission android:name="android.permission.POST_NOTIFICATIONS" />
```

3. Initialize in your `Application` subclass:

```kotlin
import ota.zen.android.ZenConfig
import ota.zen.android.ZenUpdater

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        ZenUpdater.init(
            this,
            ZenConfig(
                apiUrl = "https://your-api.vercel.app", // no /api suffix
                appId  = "com.example.app"              // your applicationId
            )
        )
    }
}
```

That's it — with the defaults the SDK checks on first Activity resume and
silently installs when the app is Device Owner (see §Device Owner), otherwise
it prompts the user.

## Option B — Library module

1. Copy `zen-android/` into your project root and add to
   `settings.gradle.kts`: `include(":zen-android")`
2. Ensure the root `build.gradle.kts` declares the plugins
   (`com.android.library`, `org.jetbrains.kotlin.android`) with versions
   `apply false`.
3. App dependency: `implementation(project(":zen-android"))`
4. Same permissions + same `init` code as Option A.

Full details: [`zen-android/README.md`](zen-android/README.md).

---

## Full code example

```kotlin
import android.app.Application
import android.app.Activity
import android.os.Bundle
import android.widget.Toast
import ota.zen.android.InstallMode
import ota.zen.android.UpdateState
import ota.zen.android.ZenConfig
import ota.zen.android.ZenUpdater

class App : Application() {
    override fun onCreate() {
        super.onCreate()
        ZenUpdater.init(
            this,
            ZenConfig(
                apiUrl = "https://your-api.vercel.app",   // Zen OTA deployment, no /api suffix
                appId = "com.example.app",                // applicationId published via `zen publish`
                channel = "stable",                       // stable | beta | alpha
                installMode = InstallMode.AUTO,           // silent if Device Owner, else prompt
                autoCheck = true,                         // check on first Activity resume
                checkIntervalMs = 4 * 60 * 60 * 1000L,    // min distance between auto checks (4h)
                allowMetered = true,                      // false = defer downloads on metered networks
                showProgressNotification = false          // low-importance download notification
            )
        )
    }
}

class MainActivity : Activity() {

    private var disposeState: (() -> Unit)? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        disposeState = ZenUpdater.onStateChanged { state, detail ->
            when (state) {
                UpdateState.CHECKING   -> showSpinner()
                UpdateState.AVAILABLE  -> {
                    // detail == "forced"  → the server marked this update REQUIRED
                    //                       (running versionCode < release.minVersionCode).
                    //                       UI MUST show a NON-DISMISSABLE dialog here.
                    if (detail == "forced") showNonDismissableUpdateDialog()
                    // detail == "waiting for an unmetered network" → download deferred
                }
                UpdateState.DOWNLOADING -> progressBar.progress = detail?.trimEnd('%')?.toIntOrNull() ?: 0
                UpdateState.DOWNLOADED -> Toast.makeText(this, "Update ready: $detail", Toast.LENGTH_SHORT).show()
                UpdateState.INSTALLING -> showInstallingScreen() // process will die after silent install
                UpdateState.UP_TO_DATE -> Unit
                UpdateState.FAILED     -> logFailure(detail)     // detail explains the reason
                UpdateState.IDLE       -> Unit
            }
        }
    }

    // Manual check — always hits the network, bypasses the 4h interval.
    private fun onRefreshClicked() = ZenUpdater.check(this)

    override fun onDestroy() {
        disposeState?.invoke()
        super.onDestroy()
    }
}
```

---

## The end-to-end flow (the product promise)

1. **App opens** → `ZenUpdater.init` runs in `Application.onCreate()`.
2. **First Activity resume** → background `GET /api/check`
   (`appId`, `channel`, `versionCode`, `deviceId`, `platform=android`).
3. **Update available** → state `AVAILABLE` → APK downloads in the background
   into `Android/data/<pkg>/files/zen-ota/<versionCode>.apk.part`
   (resumable via HTTP `Range`, even across app restarts), verified with
   SHA-256 → state `DOWNLOADED`.
4. **Install**:
   - Device Owner → **fully silent** PackageInstaller session
     (`setRequireUserAction(false)` on API 31+) → the OS **kills the process**
     and the app **reopens on the new version** — zero user interaction;
   - otherwise → PackageInstaller confirmation dialog (API 24+) or the legacy
     install intent (API 21–23).
5. **Reconciliation** → on the next launch the SDK detects the running
   `versionCode` equals the persisted pending-install record and posts
   `install_success` to `POST /api/event`; install exceptions post
   `install_failed`. The dashboard funnel (checks → downloads → installs) is
   therefore fully automatic.

Open app → background download → silent install → reopen on new version.
No Play Store, no dialogs (Device Owner), no user friction.

---

## Device Owner setup (silent installs)

Silent installs need the app to be a Device Owner (managed/kiosk devices):

```bash
adb shell dpm set-device-owner com.example.app/.ZenDeviceAdmin
adb shell dpm list owners   # verify
```

`dpm set-device-owner` requires a `DeviceAdminReceiver`. Minimal setup
(also in [`zen-android/README.md`](zen-android/README.md)):

```kotlin
class ZenDeviceAdminReceiver : android.app.admin.DeviceAdminReceiver()
```

```xml
<!-- inside <application> -->
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

Provision on a fresh device (before any account is added). The `INTERNET`
permission is the only hard requirement; `REQUEST_INSTALL_PACKAGES` is needed
for the prompt flow on API 26+, `POST_NOTIFICATIONS` only for the optional
progress notification on API 33+.

---

## Forced updates

When the running `versionCode` is below the release `minVersionCode`, the
server answers `updateRequired: true`. The SDK:

- emits `AVAILABLE` with detail exactly `"forced"`,
- downloads immediately, **ignoring metered-network preferences**.

**Contract:** the hosting UI must show a **non-dismissable** dialog for
forced updates. Use `ZenUpdater.lastReleaseInfo()` (library consumers can
also re-check on their own) to render versionName/release notes.

---

## State reference

| State | Meaning | Detail string |
|---|---|---|
| `CHECKING` | check request in flight | `null` |
| `AVAILABLE` | newer release exists | `"forced"` when required; `"waiting for an unmetered network"` when deferred; else `null` |
| `DOWNLOADING` | download in progress | `"42%"` |
| `DOWNLOADED` | APK verified on disk | new `versionName` |
| `INSTALLING` | install session committed | new `versionName` (process dies next on silent installs) |
| `UP_TO_DATE` | nothing newer | `null` |
| `FAILED` | anything failed | human-readable reason |

---

## Troubleshooting

| Symptom | Cause / fix |
|---|---|
| `check failed: HTTP 404 (unknown channel)` | channel name typo, or channel not yet published (`zen publish`). |
| `check failed: HTTP 404 (unknown appId)` | `appId` in `ZenConfig` differs from the one published. |
| `silent install requires device-owner privileges` | device not provisioned as owner (§Device Owner) or `installMode = SILENT_ONLY` on a normal device. |
| `REQUEST_INSTALL_PACKAGES permission missing` | declare the permission and grant "install unknown apps" for the app (Settings → Apps → Special access). |
| `download failed: checksum mismatch` | the published APK changed after `zen publish`; republish and the next check fetches the correct `sha256`. |
| Prompt never appears on API 31+ | app targets API 31+ and neither `REQUEST_INSTALL_PACKAGES` nor Device-Owner privileges are present. |
| Silent install succeeded but dashboard shows no install | installs are counted on the **next app open** via reconciliation — the counter appears once the updated app starts and calls `ZenUpdater.init`. |
| Progress notification missing on API 33+ | grant `POST_NOTIFICATIONS` or set `showProgressNotification = false` (the SDK skips silently otherwise). |
| Downloads keep restarting | some CDNs ignore `Range`; the SDK detects the full-body `200` and restarts cleanly — use the R2 mirror (`zen publish --r2`) which honors Range. |
| Multiple updates queued | only one download runs at a time per process; new checks for a newer version replace the in-flight metadata automatically. |
