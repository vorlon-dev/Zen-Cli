/*
 * Zen OTA — drop-in, single-file auto-update client for Android.
 * ---------------------------------------------------------------------------
 *
 * This file is the ENTIRE SDK. Copy it anywhere into your app's source tree
 * (package `ota.zen.android`) and it works: ZERO external dependencies — only
 * the Android framework (`android.*`, `org.json`), the JDK and the Kotlin
 * standard library. No external libraries at all (no support libraries, no HTTP
 * client, no coroutines), no manifest components (the install-status receiver
 * is registered dynamically).
 *
 * QUICKSTART
 * ----------
 *   class App : Application() {
 *       override fun onCreate() {
 *           super.onCreate()
 *           ZenUpdater.init(
 *               this,
 *               ZenConfig(apiUrl = "https://your-zen-ota.vercel.app", appId = "com.example.app")
 *           )
 *       }
 *   }
 *
 * REQUIRED APP PERMISSIONS (add to YOUR AndroidManifest.xml)
 * ----------------------------------------------------------
 *   <!-- Mandatory: update checks + APK downloads -->
 *   <uses-permission android:name="android.permission.INTERNET" />
 *
 *   <!-- Recommended: lets the user-confirm install flow run on API 26+. Without it
 *        the SDK emits FAILED with the reason "REQUEST_INSTALL_PACKAGES permission missing". -->
 *   <uses-permission android:name="android.permission.REQUEST_INSTALL_PACKAGES" />
 *
 *   <!-- Optional: only needed on API 33+ if ZenConfig.showProgressNotification = true.
 *        If not granted, the SDK silently skips notifications. -->
 *   <uses-permission android:name="android.permission.POST_NOTIFICATIONS" />
 *
 * SILENT INSTALLS (Device Owner)
 * ------------------------------
 * Fully silent, zero-UI installs work when your app is a Device Owner. Provision
 * via adb on a dedicated/managed device:
 *
 *   adb shell dpm set-device-owner com.example.app/.ZenDeviceAdmin
 *
 * `dpm set-device-owner` needs a DeviceAdminReceiver to exist. The minimal
 * 3-line implementation (see android/README.md for the manifest + resource bits):
 *
 *   class ZenDeviceAdminReceiver : android.app.admin.DeviceAdminReceiver()
 *
 * Non-owner devices fall back to the PackageInstaller user-confirmation dialog
 * (API 24+) or the legacy ACTION_INSTALL_PACKAGE intent (API 21-23).
 *
 * HOW IT WORKS
 * ------------
 *   app open -> GET /api/check -> updateAvailable? -> background download
 *   (resumable via HTTP Range) -> SHA-256 verify -> install:
 *       1. silent PackageInstaller session if Device Owner (process is killed
 *          by the system and the app reopens on the new version),
 *       2. PackageInstaller with user confirmation (API 24+),
 *       3. legacy install intent (API 21-23).
 *   After the update, on first launch the SDK reconciles its persisted
 *   "pending install" record with the running versionCode and reports
 *   install_success / install_failed to POST /api/event.
 *
 * Downloads live for the process lifetime and RESUME ACROSS APP RESTARTS
 * (partial `.apk.part` file + persisted in-flight metadata).
 *
 * The identical file ships as the `zen-android` Gradle library module; the two
 * copies are kept byte-identical on purpose.
 */
package ota.zen.android

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.app.Application
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.admin.DevicePolicyManager
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.util.Log
import org.json.JSONObject
import java.io.File
import java.io.FileOutputStream
import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.lang.ref.WeakReference
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder
import java.security.MessageDigest
import java.util.Properties
import java.util.UUID
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean

/**
 * How a downloaded update should be installed.
 */
enum class InstallMode {
    /** Silent install if the app is Device Owner, otherwise user-confirmation prompt. */
    AUTO,

    /** Never prompt: silent install (Device Owner only) or emit [UpdateState.FAILED]. */
    SILENT_ONLY,

    /** Never install silently: always ask the user (PackageInstaller dialog or install intent). */
    PROMPT_ONLY
}

/**
 * Lifecycle of an update, delivered to listeners registered with
 * [ZenUpdater.addListener] / [ZenUpdater.onStateChanged]. Callbacks always run
 * on the main thread.
 */
enum class UpdateState {
    /** Initial state. */
    IDLE,

    /** A check request is in flight. Detail is null. */
    CHECKING,

    /**
     * The server has a newer release. Detail is `"forced"` when the update is
     * mandatory (`updateRequired == true`, i.e. the running versionCode is below
     * the release `minVersionCode`) — the hosting UI MUST then show a
     * non-dismissable blocking dialog. Detail is `"waiting for an unmetered network"`
     * when the download was deferred by [ZenConfig.allowMetered].
     */
    AVAILABLE,

    /** Download in progress. Detail is the percentage, e.g. `"42%"`. */
    DOWNLOADING,

    /** APK downloaded and SHA-256 verified. Detail is the new versionName. */
    DOWNLOADED,

    /**
     * An install session has been committed (or the legacy install intent was
     * fired). Detail is the new versionName. For silent installs the OS kills
     * the process right after this point; the app reopens on the new build.
     */
    INSTALLING,

    /** No update available — the app runs the newest release of its channel. Detail is null. */
    UP_TO_DATE,

    /** Something failed. Detail is a human-readable reason (never null). */
    FAILED
}

/**
 * Immutable SDK configuration, supplied to [ZenUpdater.init].
 *
 * @property apiUrl                  Zen OTA API base URL, e.g. `"https://zen-ota.vercel.app"`.
 *                                   A trailing `/` or a trailing `/api` is tolerated and stripped.
 * @property appId                   applicationId of the host app, e.g. `"com.example.app"`.
 * @property channel                 release channel: `stable` (default), `beta` or `alpha`.
 * @property installMode             install strategy, see [InstallMode].
 * @property autoCheck               when true (default), the SDK checks for updates on the first
 *                                   Activity resume after process start.
 * @property checkIntervalMs         minimum wall-clock distance between *automatic* network checks.
 *                                   The last check time is persisted, so it also spans process
 *                                   restarts. Manual [ZenUpdater.check] calls always bypass this
 *                                   interval. Default: 4 hours.
 * @property allowMetered            when false, downloads are deferred while the active network is
 *                                   metered (forced/`updateRequired` downloads ignore this).
 * @property showProgressNotification when true, a low-importance progress notification is shown
 *                                   during downloads. Requires POST_NOTIFICATIONS on API 33+;
 *                                   silently skipped when the permission is missing. Uses a
 *                                   framework icon (`android.R.drawable.stat_sys_download`).
 */
data class ZenConfig(
    val apiUrl: String,
    val appId: String,
    val channel: String = "stable",
    val installMode: InstallMode = InstallMode.AUTO,
    val autoCheck: Boolean = true,
    val checkIntervalMs: Long = 4 * 60 * 60 * 1000L,
    val allowMetered: Boolean = true,
    val showProgressNotification: Boolean = false
)

/**
 * Zen OTA updater. See the file header for integration instructions.
 *
 * Threading model: every network/install operation runs on dedicated
 * single-thread executors (surviving Activity changes — this object lives for
 * the whole process). Listener callbacks are marshalled to the main thread.
 *
 * Process death: a silent install kills the process after [UpdateState.INSTALLING].
 * The next launch reconciles the persisted pending-install record with the new
 * running versionCode and reports `install_success` to the event endpoint.
 */
object ZenUpdater {

    /** Log tag used for all SDK output. */
    const val TAG = "ZenOTA"

    private const val PREFS_FILE = "zen_ota_prefs"
    private const val DOWNLOAD_DIR = "zen-ota"
    private const val APK_MIME_TYPE = "application/vnd.android.package-archive"
    private const val ACTION_INSTALL_STATUS = "ota.zen.android.action.INSTALL_STATUS"
    private const val USER_AGENT = "ZenOTA/1.0 (Android)"
    private const val HTTP_TIMEOUT_MS = 10_000
    private const val DOWNLOAD_READ_TIMEOUT_MS = 30_000
    private const val PROGRESS_EMIT_INTERVAL_MS = 800L
    private const val NOTIF_CHANNEL_ID = "zen_ota_progress"
    private const val NOTIF_CHANNEL_NAME = "Zen OTA updates"
    private const val NOTIF_ID = 5317
    private const val EVENT_INSTALL_SUCCESS = "install_success"
    private const val EVENT_INSTALL_FAILED = "install_failed"
    private const val HEX = "0123456789abcdef"

    // ---- persisted preference keys (file "zen_ota_prefs") --------------------
    private const val KEY_DEVICE_ID = "deviceId"
    private const val KEY_LAST_CHECK = "lastCheckAt"
    private const val KEY_PENDING_VC = "pendingInstallVersionCode"
    private const val KEY_PENDING_NAME = "pendingInstallVersionName"
    private const val KEY_PENDING_SHA = "pendingInstallSha256"
    private const val KEY_DOWNLOADED_VC = "downloadedVersionCode"
    private const val KEY_DOWNLOADED_SHA = "downloadedSha256"
    private const val KEY_INFLIGHT_VC = "inFlightVersionCode"
    private const val KEY_INFLIGHT_URL = "inFlightDownloadUrl"
    private const val KEY_INFLIGHT_SHA = "inFlightSha256"
    private const val KEY_INFLIGHT_SIZE = "inFlightSize"
    private const val KEY_INFLIGHT_NAME = "inFlightVersionName"

    private const val ARTIFACT_PATTERN = """(\d+)\.apk(\.part)?"""

    private val lock = Any()
    private val mainHandler = Handler(Looper.getMainLooper())
    private val listeners = CopyOnWriteArrayList<(UpdateState, String?) -> Unit>()
    private val autoCheckFired = AtomicBoolean(false)
    private val artifactRegex = Regex(ARTIFACT_PATTERN)

    @Volatile private var appContext: Context? = null
    @Volatile private var config: ZenConfig? = null
    @Volatile private var prefs: ZenPrefs? = null
    @Volatile private var ioExecutor: ExecutorService = newExecutor("ZenOTA-io")
    @Volatile private var downloadExecutor: ExecutorService = newExecutor("ZenOTA-worker")
    @Volatile private var downloadingVersionCode: Long = -1L
    @Volatile private var installingVersionCode: Long = -1L
    @Volatile private var activityRef: WeakReference<Activity>? = null
    @Volatile private var lastRelease: ReleaseInfo? = null

    // -------------------------------------------------------------------------
    // Public API
    // -------------------------------------------------------------------------

    /**
     * Initializes the SDK. Call exactly once from `Application.onCreate()`.
     *
     * - stores the (normalized) config in this singleton,
     * - generates and persists a random UUID device id on first run
     *   (file-based private storage via [Context.openFileOutput], key `"deviceId"`),
     * - registers [Application.ActivityLifecycleCallbacks]: on the first
     *   `onActivityResumed` after process start, [maybeCheck] runs (respecting
     *   [ZenConfig.autoCheck] and [ZenConfig.checkIntervalMs]),
     * - reconciles a pending install from a previous process: if the running
     *   versionCode matches the persisted pending install, `install_success`
     *   is reported to `POST /api/event` (fire-and-forget),
     * - cleans stale downloaded artifacts and resumes an interrupted download
     *   left over from a previous process.
     *
     * @throws IllegalArgumentException when [ZenConfig.apiUrl] or [ZenConfig.appId] is blank.
     */
    fun init(application: Application, config: ZenConfig) {
        require(config.apiUrl.isNotBlank()) { "ZenConfig.apiUrl must not be blank" }
        require(config.appId.isNotBlank()) { "ZenConfig.appId must not be blank" }
        val ctx = application.applicationContext
        val normalized = config.copy(
            apiUrl = config.apiUrl.trim().trimEnd('/').removeSuffix("/api")
        )
        synchronized(lock) {
            appContext = ctx
            this.config = normalized
            prefs = ZenPrefs(ctx)
        }
        application.registerActivityLifecycleCallbacks(lifecycleCallbacks)
        deviceId(ctx) // ensure the id exists and is persisted right away
        ioExecutor.execute {
            reconcileInstall(ctx)
            cleanupStaleFiles(ctx)
            resumeInFlightDownload(ctx)
        }
    }

    /**
     * Manually checks for updates. Bypasses [ZenConfig.checkIntervalMs] and the
     * auto-check-on-first-resume gate (a manual call counts as the first check
     * of the process, so no duplicate auto check is fired).
     *
     * On `updateAvailable` the SDK emits [UpdateState.AVAILABLE] and starts the
     * download on a background thread, then installs per [ZenConfig.installMode].
     *
     * @param activity optional Activity, kept as a weak reference; when present,
     *                 user-confirmation dialogs are launched from it (better UX)
     *                 instead of the application context with `NEW_TASK`.
     */
    fun check(activity: Activity? = null) {
        val cfg = config
        if (cfg == null) {
            Log.e(TAG, "check() called before init(); ignoring")
            emitState(UpdateState.FAILED, "ZenUpdater.init() must be called before check()")
            return
        }
        activityRef = if (activity != null) WeakReference(activity) else null
        autoCheckFired.set(true)
        ioExecutor.execute {
            prefs?.putLong(KEY_LAST_CHECK, System.currentTimeMillis())
            performCheck()
        }
    }

    /**
     * Automatic check triggered by the first Activity resume (and usable from tests).
     * Respects [ZenConfig.autoCheck] and [ZenConfig.checkIntervalMs]; no-ops when
     * the interval has not elapsed yet. Runs on a background thread.
     */
    internal fun maybeCheck() {
        val cfg = config ?: return
        val p = prefs ?: return
        ioExecutor.execute {
            val now = System.currentTimeMillis()
            val last = p.getLong(KEY_LAST_CHECK)
            if (last > 0L && now - last < cfg.checkIntervalMs) {
                Log.d(TAG, "auto check skipped: interval not elapsed (${now - last} < ${cfg.checkIntervalMs})")
                return@execute
            }
            p.putLong(KEY_LAST_CHECK, now)
            performCheck()
        }
    }

    /**
     * Registers a state listener. Callbacks are invoked on the main thread;
     * the `String?` parameter carries state-specific detail (see [UpdateState]).
     * Exceptions thrown by listeners are caught and logged, never propagated.
     */
    fun addListener(listener: (UpdateState, String?) -> Unit) {
        listeners.add(listener)
    }

    /** Removes a previously registered listener. */
    fun removeListener(listener: (UpdateState, String?) -> Unit) {
        listeners.remove(listener)
    }

    /**
     * Registers a state listener and returns an unregister closure:
     * `val dispose = ZenUpdater.onStateChanged { state, detail -> ... }`.
     */
    fun onStateChanged(listener: (UpdateState, String?) -> Unit): () -> Unit {
        addListener(listener)
        return { removeListener(listener) }
    }

    /**
     * Returns the stable random device UUID used for rollout bucketing,
     * generating and persisting it on first call (app-private file storage).
     */
    fun deviceId(context: Context): String {
        val ctx = context.applicationContext
        val p = prefs ?: ZenPrefs(ctx).also { prefs = it }
        val existing = p.get(KEY_DEVICE_ID)
        if (!existing.isNullOrBlank()) return existing
        synchronized(lock) {
            val again = p.get(KEY_DEVICE_ID)
            if (!again.isNullOrBlank()) return again
            val fresh = UUID.randomUUID().toString()
            p.put(KEY_DEVICE_ID, fresh)
            Log.d(TAG, "generated new deviceId=$fresh")
            return fresh
        }
    }

    /** Most recently seen update release, non-null after [UpdateState.AVAILABLE]. */
    internal fun lastReleaseInfo(): ReleaseInfo? = lastRelease

    /**
     * Resets all in-memory state (config, listeners, executors, lifecycle
     * callbacks). Test-only convenience; not part of the public contract.
     */
    internal fun resetForTests() {
        synchronized(lock) {
            val ctx = appContext
            if (ctx != null) runCatching { ctx.unregisterActivityLifecycleCallbacks(lifecycleCallbacks) }
            runCatching { ioExecutor.shutdownNow() }
            runCatching { downloadExecutor.shutdownNow() }
            ioExecutor = newExecutor("ZenOTA-io")
            downloadExecutor = newExecutor("ZenOTA-worker")
            appContext = null
            config = null
            prefs = null
            downloadingVersionCode = -1L
            installingVersionCode = -1L
            activityRef = null
            lastRelease = null
            autoCheckFired.set(false)
            listeners.clear()
        }
    }

    // -------------------------------------------------------------------------
    // Lifecycle-driven auto check
    // -------------------------------------------------------------------------

    private val lifecycleCallbacks = object : Application.ActivityLifecycleCallbacks {
        override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) = Unit

        override fun onActivityStarted(activity: Activity) = Unit

        override fun onActivityResumed(activity: Activity) {
            if (autoCheckFired.compareAndSet(false, true)) {
                val cfg = config ?: return
                if (!cfg.autoCheck) return
                Log.d(TAG, "first activity resumed; running auto check")
                maybeCheck()
            }
        }

        override fun onActivityPaused(activity: Activity) = Unit

        override fun onActivityStopped(activity: Activity) = Unit

        override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) = Unit

        override fun onActivityDestroyed(activity: Activity) = Unit
    }

    // -------------------------------------------------------------------------
    // Check flow
    // -------------------------------------------------------------------------

    private fun performCheck() {
        val cfg = config ?: return
        val ctx = appContext ?: return
        Log.i(TAG, "checking for updates: appId=${cfg.appId} channel=${cfg.channel}")
        emitState(UpdateState.CHECKING, null)
        var conn: HttpURLConnection? = null
        try {
            val url = buildCheckUrl(cfg, deviceId(ctx), currentVersionCode(ctx))
            conn = openHttpConnection(url)
            val code = conn.responseCode
            if (code != HttpURLConnection.HTTP_OK) {
                val err = parseApiError(readTextSafe(conn.errorStream))
                Log.w(TAG, "check failed: HTTP $code $err")
                val suffix = if (err.isNotBlank()) " ($err)" else ""
                emitState(UpdateState.FAILED, "check failed: HTTP $code$suffix")
                return
            }
            handleCheckResult(ctx, parseCheckResponse(readTextSafe(conn.inputStream)))
        } catch (t: Throwable) {
            Log.e(TAG, "check failed", t)
            emitState(UpdateState.FAILED, "check failed: ${t.message ?: t.javaClass.simpleName}")
        } finally {
            conn?.disconnect()
        }
    }

    private fun buildCheckUrl(cfg: ZenConfig, deviceId: String, versionCode: Long): String {
        val sb = StringBuilder(cfg.apiUrl).append("/api/check")
        sb.append("?appId=").append(URLEncoder.encode(cfg.appId, "UTF-8"))
        sb.append("&channel=").append(URLEncoder.encode(cfg.channel, "UTF-8"))
        sb.append("&versionCode=").append(versionCode)
        sb.append("&deviceId=").append(URLEncoder.encode(deviceId, "UTF-8"))
        sb.append("&platform=android")
        return sb.toString()
    }

    private fun handleCheckResult(ctx: Context, result: CheckResult) {
        val cfg = config ?: return
        val current = currentVersionCode(ctx)
        val release = result.release
        if (!result.updateAvailable || release == null || release.versionCode <= current) {
            if (!isDownloading()) clearInFlight()
            emitState(UpdateState.UP_TO_DATE, null)
            return
        }
        lastRelease = release
        val forced = result.updateRequired || current < release.minVersionCode
        emitState(UpdateState.AVAILABLE, if (forced) "forced" else null)

        // Fast path: this exact release was already downloaded and verified.
        if (isAlreadyDownloaded(release, ctx)) {
            Log.i(TAG, "release ${release.versionCode} already downloaded; installing")
            emitState(UpdateState.DOWNLOADED, release.versionName)
            installDownloaded(release)
            return
        }
        if (isDownloadingVersion(release.versionCode)) return

        // The server moved on to a different release than the one we were resuming.
        val inFlight = prefs?.getLong(KEY_INFLIGHT_VC) ?: 0L
        if (inFlight > 0L && inFlight != release.versionCode) clearInFlight()

        if (!cfg.allowMetered && !forced && isMetered(ctx)) {
            Log.i(TAG, "on metered network; deferring download (allowMetered=false)")
            emitState(UpdateState.AVAILABLE, "waiting for an unmetered network")
            return
        }
        startDownload(release, ctx)
    }

    // -------------------------------------------------------------------------
    // Download flow
    // -------------------------------------------------------------------------

    /**
     * Starts (or resumes) the download of [release] on the download executor.
     * The in-flight metadata is persisted first so a process restart can pick
     * the download back up (see [resumeInFlightDownload]).
     */
    private fun startDownload(release: ReleaseInfo, ctx: Context) {
        synchronized(lock) {
            if (downloadingVersionCode == release.versionCode) return
            downloadingVersionCode = release.versionCode
        }
        val url = release.downloadUrl.ifBlank { release.directUrl }
        prefs?.let { p ->
            p.putLong(KEY_INFLIGHT_VC, release.versionCode)
            p.put(KEY_INFLIGHT_URL, url)
            p.put(KEY_INFLIGHT_SHA, release.sha256)
            p.putLong(KEY_INFLIGHT_SIZE, release.size)
            p.put(KEY_INFLIGHT_NAME, release.versionName)
        }
        downloadExecutor.execute {
            try {
                doDownload(ctx, release)
            } finally {
                synchronized(lock) {
                    if (downloadingVersionCode == release.versionCode) downloadingVersionCode = -1L
                }
            }
        }
    }

    /**
     * Streams the APK into `<external-files>/zen-ota/<versionCode>.apk.part`
     * with HTTP Range resume support, verifying SHA-256 incrementally while
     * writing. On success renames to `<versionCode>.apk`, emits
     * [UpdateState.DOWNLOADED] and hands over to the install chain.
     *
     * IO errors keep the `.part` file (the next app launch resumes via
     * [resumeInFlightDownload]); checksum mismatches delete it.
     */
    private fun doDownload(ctx: Context, release: ReleaseInfo) {
        Log.i(TAG, "download starting for versionCode=${release.versionCode}")
        emitState(UpdateState.DOWNLOADING, "0%")
        showProgressNotification(ctx, 0)
        var conn: HttpURLConnection? = null
        try {
            val url = release.downloadUrl.ifBlank { release.directUrl }
            if (url.isBlank()) throw IOException("release ${release.versionCode} has no downloadUrl")
            val dir = downloadDir(ctx)
            val part = File(dir, "${release.versionCode}.apk.part")

            var resumeFrom = 0L
            if (part.exists() && part.length() > 0L) resumeFrom = part.length()

            conn = openHttpConnection(url, readTimeoutMs = DOWNLOAD_READ_TIMEOUT_MS)
            if (resumeFrom > 0L) conn.setRequestProperty("Range", "bytes=$resumeFrom-")
            val code = conn.responseCode

            var appending = false
            when {
                code == HttpURLConnection.HTTP_RANGE_NOT_SATISFIABLE && resumeFrom > 0L -> {
                    // The .part file is already fully downloaded (crash between the
                    // end of the download and the rename). Finalize if it verifies.
                    val existing = sha256OfFile(part)
                    if (release.sha256.isBlank() || existing.equals(release.sha256.trim(), ignoreCase = true)) {
                        Log.i(TAG, "HTTP 416: partial file already complete; finalizing")
                        finalizeDownload(ctx, part, release, existing.lowercase())
                        return
                    }
                    part.delete()
                    throw IOException("HTTP 416 and partial file hash mismatch; removed stale .part")
                }
                code == HttpURLConnection.HTTP_PARTIAL && resumeFrom > 0L -> appending = true
                code in 200..299 && resumeFrom > 0L -> {
                    // Server ignored the Range header and restarted from byte 0.
                    Log.d(TAG, "server ignored Range header; restarting download")
                    part.delete()
                    resumeFrom = 0L
                }
                code in 200..299 -> Unit // clean fresh download
                else -> throw IOException("download failed: HTTP $code")
            }

            // Determine the total size for progress reporting.
            val reported: Long = if (code == HttpURLConnection.HTTP_PARTIAL) {
                conn.getHeaderField("Content-Range")?.substringAfter('/')?.toLongOrNull()
                    ?: (resumeFrom + conn.contentLengthLong)
            } else {
                conn.contentLengthLong
            }
            val totalBytes = when {
                reported > 0L -> reported
                release.size > 0L -> release.size
                else -> -1L
            }

            val digest = MessageDigest.getInstance("SHA-256")
            if (appending) {
                // Rebuild the digest state over the bytes already on disk so the
                // final hash covers the whole file, not just the resumed tail.
                digestStream(part.inputStream(), digest)
            }

            val input = conn.inputStream
            val output = FileOutputStream(part, appending)
            val buffer = ByteArray(64 * 1024)
            var written = if (appending) resumeFrom else 0L
            var lastEmit = System.currentTimeMillis()
            input.use { ins ->
                output.use { outs ->
                    while (true) {
                        val n = ins.read(buffer)
                        if (n < 0) break
                        outs.write(buffer, 0, n)
                        outs.flush()
                        digest.update(buffer, 0, n)
                        written += n
                        val now = System.currentTimeMillis()
                        if (now - lastEmit >= PROGRESS_EMIT_INTERVAL_MS) {
                            lastEmit = now
                            val pct = if (totalBytes > 0L) ((written * 100L) / totalBytes).toInt().coerceIn(0, 100) else -1
                            emitState(UpdateState.DOWNLOADING, if (pct >= 0) "$pct%" else "downloading")
                            showProgressNotification(ctx, pct)
                        }
                    }
                }
            }
            if (totalBytes > 0L && written < totalBytes) {
                throw IOException("incomplete download: $written of $totalBytes bytes")
            }

            val actualSha = toHex(digest.digest())
            if (release.sha256.isNotBlank()) {
                if (!actualSha.equals(release.sha256.trim(), ignoreCase = true)) {
                    part.delete()
                    clearInFlight()
                    Log.e(TAG, "sha256 mismatch for versionCode=${release.versionCode}: " +
                        "expected=${release.sha256} actual=$actualSha")
                    emitState(UpdateState.FAILED, "download failed: checksum mismatch")
                    return
                }
            } else {
                Log.w(TAG, "release ${release.versionCode} has no sha256; skipping integrity verification")
            }
            finalizeDownload(ctx, part, release, actualSha)
        } catch (t: Throwable) {
            Log.e(TAG, "download of versionCode=${release.versionCode} failed", t)
            cancelProgressNotification(ctx)
            // Keep the .part file: the next launch resumes via HTTP Range
            // (resumeInFlightDownload). No automatic in-process retry — the
            // next app open (or next check) retries naturally.
            emitState(UpdateState.FAILED, "download failed: ${t.message ?: t.javaClass.simpleName}")
        } finally {
            conn?.disconnect()
        }
    }

    /** Renames the verified `.part` file and kicks off the install chain. */
    private fun finalizeDownload(ctx: Context, part: File, release: ReleaseInfo, actualSha: String) {
        val finalFile = File(part.parentFile, "${release.versionCode}.apk")
        if (finalFile.exists() && !finalFile.delete()) throw IOException("could not replace ${finalFile.name}")
        if (!part.renameTo(finalFile)) throw IOException("could not finalize ${finalFile.name}")
        prefs?.let { p ->
            p.putLong(KEY_INFLIGHT_VC, 0L)
            p.putLong(KEY_DOWNLOADED_VC, release.versionCode)
            p.put(KEY_DOWNLOADED_SHA, actualSha.lowercase())
        }
        cancelProgressNotification(ctx)
        Log.i(TAG, "download complete: ${finalFile.name} (${actualSha.take(12)}…)")
        emitState(UpdateState.DOWNLOADED, release.versionName)
        installDownloaded(release)
    }

    /**
     * Reconnects an interrupted download after a process restart: the in-flight
     * metadata persisted by [startDownload] is replayed against the leftover
     * `.apk.part` file. Called once per process from [init].
     */
    private fun resumeInFlightDownload(ctx: Context) {
        val p = prefs ?: return
        val cfg = config ?: return
        val vc = p.getLong(KEY_INFLIGHT_VC)
        if (vc <= 0L || vc <= currentVersionCode(ctx)) return
        if (apkFileFor(ctx, vc).exists()) {
            p.putLong(KEY_INFLIGHT_VC, 0L)
            return
        }
        val url = p.get(KEY_INFLIGHT_URL)
        if (url.isNullOrBlank()) return
        if (!cfg.allowMetered && isMetered(ctx)) {
            Log.i(TAG, "interrupted download deferred: metered network")
            return
        }
        val release = ReleaseInfo(
            versionName = p.get(KEY_INFLIGHT_NAME) ?: "",
            versionCode = vc,
            sha256 = p.get(KEY_INFLIGHT_SHA) ?: "",
            size = p.getLong(KEY_INFLIGHT_SIZE),
            releaseNotes = "",
            downloadUrl = url,
            directUrl = "",
            minVersionCode = 0L
        )
        Log.i(TAG, "resuming interrupted download for versionCode=$vc")
        startDownload(release, ctx)
    }

    private fun isAlreadyDownloaded(release: ReleaseInfo, ctx: Context): Boolean {
        val p = prefs ?: return false
        if (p.getLong(KEY_DOWNLOADED_VC) != release.versionCode) return false
        if (release.sha256.isNotBlank() &&
            !release.sha256.trim().equals(p.get(KEY_DOWNLOADED_SHA) ?: "", ignoreCase = true)
        ) {
            return false
        }
        return apkFileFor(ctx, release.versionCode).exists()
    }

    private fun isDownloading(): Boolean = synchronized(lock) { downloadingVersionCode > 0L }

    private fun isDownloadingVersion(versionCode: Long): Boolean =
        synchronized(lock) { downloadingVersionCode == versionCode }

    private fun clearInFlight() {
        prefs?.putLong(KEY_INFLIGHT_VC, 0L)
    }

    // -------------------------------------------------------------------------
    // Install chain: silent (Device Owner) -> prompt (PackageInstaller) -> legacy intent
    // -------------------------------------------------------------------------

    /**
     * Runs the install strategy chain for a verified APK:
     *
     * 1. **Silent** — Device Owner only: PackageInstaller session committed with
     *    `setRequireUserAction(false)` (API 31+ guard; on API 21-30 Device-Owner
     *    sessions commit silently by default). The system kills the process
     *    immediately after commit.
     * 2. **Prompt** — API 24+: PackageInstaller session with the platform's user
     *    confirmation dialog. Requires `REQUEST_INSTALL_PACKAGES` on API 26+
     *    (checked with [PackageManager.canRequestPackageInstalls]).
     * 3. **Legacy intent** — API 21-23 only: `ACTION_INSTALL_PACKAGE` with a
     *    `file://` URI (safe pre-N; on API 24+ this intent form is not allowed
     *    by the platform, so a failed session there results in [UpdateState.FAILED]).
     */
    private fun installDownloaded(release: ReleaseInfo) {
        val ctx = appContext ?: return
        val cfg = config ?: return
        val apk = apkFileFor(ctx, release.versionCode)
        if (!apk.exists()) {
            Log.e(TAG, "downloaded apk missing: ${apk.absolutePath}")
            emitState(UpdateState.FAILED, "install failed: downloaded apk is missing")
            return
        }
        synchronized(lock) {
            if (installingVersionCode == release.versionCode) {
                Log.d(TAG, "install of versionCode=${release.versionCode} already committed this " +
                    "process; restart the app to retry")
                return
            }
            installingVersionCode = release.versionCode
        }
        when (cfg.installMode) {
            InstallMode.PROMPT_ONLY -> promptChain(release, apk)
            InstallMode.SILENT_ONLY -> {
                if (!isDeviceOwner(ctx)) {
                    failInstall(release, "silent install requires device-owner privileges " +
                        "(adb shell dpm set-device-owner …)")
                    return
                }
                try {
                    installViaSession(release, apk, silent = true)
                } catch (t: Throwable) {
                    Log.e(TAG, "silent install failed", t)
                    failInstall(release, "silent install failed: ${t.message ?: t.javaClass.simpleName}")
                }
            }
            InstallMode.AUTO -> {
                if (isDeviceOwner(ctx)) {
                    try {
                        installViaSession(release, apk, silent = true)
                    } catch (t: Throwable) {
                        Log.w(TAG, "silent install failed; falling back to prompt", t)
                        promptChain(release, apk)
                    }
                } else {
                    promptChain(release, apk)
                }
            }
        }
    }

    /** User-confirmation install path (strategies 2 and 3). */
    private fun promptChain(release: ReleaseInfo, apk: File) {
        val ctx = appContext ?: return
        if (Build.VERSION.SDK_INT >= 26 && !isDeviceOwner(ctx) &&
            !ctx.packageManager.canRequestPackageInstalls()
        ) {
            failInstall(
                release,
                "REQUEST_INSTALL_PACKAGES permission missing (declare it and grant " +
                    "\"install unknown apps\" for this app)"
            )
            return
        }
        if (Build.VERSION.SDK_INT >= 24) {
            try {
                installViaSession(release, apk, silent = false)
            } catch (t: Throwable) {
                Log.e(TAG, "PackageInstaller prompt install failed", t)
                failInstall(release, "install failed: ${t.message ?: t.javaClass.simpleName}")
            }
        } else {
            try {
                installViaLegacyIntent(apk)
            } catch (t: Throwable) {
                Log.e(TAG, "legacy intent install failed", t)
                failInstall(release, "install failed: ${t.message ?: t.javaClass.simpleName}")
            }
        }
    }

    /**
     * Installs the APK through a PackageInstaller session.
     *
     * The pending-install record is persisted BEFORE `commit()` so the next
     * process launch can reconcile it (see [reconcileInstall]). A dynamic
     * broadcast receiver is registered for the session status callback; on
     * `STATUS_PENDING_USER_ACTION` it launches the platform confirmation dialog.
     *
     * After `commit()` the OS takes over: for silent installs the process is
     * killed and the app reopens on the new version.
     */
    private fun installViaSession(release: ReleaseInfo, apk: File, silent: Boolean) {
        val ctx = appContext ?: throw IOException("ZenUpdater is not initialized")
        val installer = ctx.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL)
        params.setSize(apk.length())
        if (silent && Build.VERSION.SDK_INT >= 31) {
            // API 31+: without this flag even device-owner sessions may show UI.
            // On API 21-30 device-owner sessions commit silently by default.
            params.setRequireUserAction(false)
        }
        val sessionId = installer.createSession(params)
        var session: PackageInstaller.Session? = null
        try {
            session = installer.openSession(sessionId)
            val out: OutputStream = session.openWrite("zenota_${release.versionCode}.apk", 0L, apk.length())
            try {
                apk.inputStream().use { input ->
                    val buffer = ByteArray(64 * 1024)
                    while (true) {
                        val n = input.read(buffer)
                        if (n < 0) break
                        out.write(buffer, 0, n)
                    }
                    out.flush()
                }
                session.fsync(out)
            } finally {
                runCatching { out.close() }
            }

            // Persist BEFORE commit: the process may die within milliseconds.
            prefs?.let { p ->
                p.putLong(KEY_PENDING_VC, release.versionCode)
                p.put(KEY_PENDING_NAME, release.versionName)
                p.put(KEY_PENDING_SHA, release.sha256)
            }
            registerInstallStatusReceiver(ctx, sessionId, release)

            // The system writes status extras into this broadcast, so the
            // PendingIntent must be mutable on API 31+ (FLAG_MUTABLE).
            val piFlags = if (Build.VERSION.SDK_INT >= 31) {
                PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_MUTABLE
            } else {
                PendingIntent.FLAG_UPDATE_CURRENT
            }
            val statusIntent = Intent(ACTION_INSTALL_STATUS).setPackage(ctx.packageName)
            val pendingIntent = PendingIntent.getBroadcast(ctx, sessionId, statusIntent, piFlags)
            session.commit(pendingIntent.intentSender)
            Log.i(TAG, "install session $sessionId committed (silent=$silent); " +
                "the system may kill this process now")
            emitState(UpdateState.INSTALLING, release.versionName)
        } catch (t: Throwable) {
            runCatching { session?.abandon() }
            throw t
        } finally {
            runCatching { session?.close() }
        }
    }

    /**
     * Registers the session-status receiver right before commit. The receiver
     * unregisters itself after the first broadcast; if the process dies before
     * the broadcast (normal for silent installs), the next-launch
     * [reconcileInstall] is the authoritative success signal.
     */
    private fun registerInstallStatusReceiver(ctx: Context, sessionId: Int, release: ReleaseInfo) {
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(receiveContext: Context, intent: Intent) {
                runCatching { ctx.unregisterReceiver(this) }
                val status = intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)
                val message = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)
                if (status == PackageInstaller.STATUS_PENDING_USER_ACTION) {
                    val confirm = extraIntent(intent, Intent.EXTRA_INTENT)
                    if (confirm != null) {
                        Log.d(TAG, "session $sessionId: launching user confirmation dialog")
                        startActivityCompat(ctx, confirm)
                    }
                    return // user still has to act; keep the pending-install record
                }
                if (status == PackageInstaller.STATUS_SUCCESS) {
                    Log.i(TAG, "install session $sessionId succeeded")
                    return
                }
                Log.w(TAG, "install session $sessionId failed: status=$status message=$message")
                failInstall(release, "install failed: ${message ?: "PackageInstaller status $status"}")
            }
        }
        val filter = IntentFilter(ACTION_INSTALL_STATUS)
        if (Build.VERSION.SDK_INT >= 33) {
            // Status broadcasts are sent by the system through a PendingIntent owned
            // by this app, so NOT_EXPORTED still receives them. On OEM builds that
            // misbehave, next-launch reconciliation still reports success.
            ctx.registerReceiver(receiver, filter, Context.RECEIVER_NOT_EXPORTED)
        } else {
            @SuppressLint("UnspecifiedRegisterReceiverFlag")
            ctx.registerReceiver(receiver, filter)
        }
    }

    /**
     * API 21-23 fallback: plain `ACTION_INSTALL_PACKAGE` with a `file://` URI.
     * Legal only below Android N (24+) where `FileUriExposedException` does not
     * exist — on 24+ the PackageInstaller prompt path is used instead.
     */
    @Suppress("DEPRECATION") // ACTION_INSTALL_PACKAGE is the correct mechanism on API 21-23
    private fun installViaLegacyIntent(apk: File) {
        val ctx = appContext ?: return
        val intent = Intent(Intent.ACTION_INSTALL_PACKAGE).apply {
            setDataAndType(Uri.fromFile(apk), APK_MIME_TYPE)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        emitState(UpdateState.INSTALLING, null)
        startActivityCompat(ctx, intent)
    }

    /** Emits [UpdateState.FAILED], reports `install_failed` and re-arms the install gate. */
    private fun failInstall(release: ReleaseInfo, message: String) {
        emitState(UpdateState.FAILED, message)
        postEvent(EVENT_INSTALL_FAILED, release.versionCode, message.take(300))
        synchronized(lock) {
            if (installingVersionCode == release.versionCode) installingVersionCode = -1L
        }
    }

    // -------------------------------------------------------------------------
    // Install reconciliation + housekeeping
    // -------------------------------------------------------------------------

    /**
     * Runs once per process: if the persisted pending-install versionCode equals
     * the running versionCode, the previous install succeeded — reports
     * `install_success` and clears the record. If the running versionCode moved
     * past the pending one (e.g. the user installed via a store), the record is
     * cleared silently. A pending versionCode above the running one means the
     * install is still outstanding (e.g. the confirmation dialog was never
     * answered) and is kept for future reconciliation.
     */
    private fun reconcileInstall(ctx: Context) {
        val p = prefs ?: return
        val pending = p.getLong(KEY_PENDING_VC)
        if (pending <= 0L) return
        val current = currentVersionCode(ctx)
        when {
            current == pending -> {
                p.putLong(KEY_PENDING_VC, 0L)
                Log.i(TAG, "install of versionCode=$pending confirmed; reporting install_success")
                postEvent(EVENT_INSTALL_SUCCESS, current, p.get(KEY_PENDING_NAME))
            }
            current > pending -> {
                Log.i(TAG, "clearing stale pending install record (pending=$pending current=$current)")
                p.putLong(KEY_PENDING_VC, 0L)
            }
            else -> Log.d(TAG, "install still pending (pending=$pending current=$current)")
        }
    }

    /** Deletes downloaded artifacts that can no longer be useful (already installed). */
    private fun cleanupStaleFiles(ctx: Context) {
        val dir = downloadDir(ctx)
        val files = dir.listFiles() ?: return
        val current = currentVersionCode(ctx)
        for (file in files) {
            val match = artifactRegex.matchEntire(file.name) ?: continue
            val code = match.groupValues[1].toLongOrNull() ?: continue
            if (code <= current && file.isFile) {
                Log.d(TAG, "deleting stale artifact ${file.name}")
                file.delete()
            }
        }
    }

    // -------------------------------------------------------------------------
    // Event reporting
    // -------------------------------------------------------------------------

    /** Fire-and-forget `POST /api/event`; all errors are logged and swallowed. */
    private fun postEvent(type: String, versionCode: Long, detail: String?) {
        ioExecutor.execute {
            runCatching { postEventSync(type, versionCode, detail) }
                .onFailure { Log.w(TAG, "event post failed", it) }
        }
    }

    private fun postEventSync(type: String, versionCode: Long, detail: String?) {
        val cfg = config ?: return
        val ctx = appContext ?: return
        val body = JSONObject().apply {
            put("appId", cfg.appId)
            put("channel", cfg.channel)
            put("deviceId", deviceId(ctx))
            put("versionCode", versionCode)
            put("type", type)
            if (!detail.isNullOrBlank()) put("detail", detail)
        }
        val conn = openHttpConnection("${cfg.apiUrl}/api/event")
        try {
            conn.requestMethod = "POST"
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json; charset=utf-8")
            conn.outputStream.use { os ->
                os.write(body.toString().toByteArray(Charsets.UTF_8))
                os.flush()
            }
            val code = conn.responseCode
            if (code != HttpURLConnection.HTTP_NO_CONTENT) {
                Log.w(TAG, "event endpoint returned HTTP $code (type=$type)")
            } else {
                Log.d(TAG, "event reported: type=$type versionCode=$versionCode")
            }
        } finally {
            conn.disconnect()
        }
    }

    // -------------------------------------------------------------------------
    // Progress notification (optional, framework-only)
    // -------------------------------------------------------------------------

    private fun showProgressNotification(ctx: Context, percent: Int) {
        val cfg = config ?: return
        if (!cfg.showProgressNotification) return
        if (Build.VERSION.SDK_INT >= 33 &&
            ctx.checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            Log.d(TAG, "POST_NOTIFICATIONS not granted; skipping progress notification")
            return
        }
        runCatching {
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            if (Build.VERSION.SDK_INT >= 26) {
                val channel = NotificationChannel(NOTIF_CHANNEL_ID, NOTIF_CHANNEL_NAME, NotificationManager.IMPORTANCE_LOW)
                nm.createNotificationChannel(channel)
            }
            val builder: Notification.Builder = if (Build.VERSION.SDK_INT >= 26) {
                Notification.Builder(ctx, NOTIF_CHANNEL_ID)
            } else {
                @Suppress("DEPRECATION")
                Notification.Builder(ctx)
            }
            builder.setSmallIcon(android.R.drawable.stat_sys_download) // swap for your own icon if desired
                .setContentTitle("Downloading update")
                .setOnlyAlertOnce(true)
                .setOngoing(true)
            if (percent >= 0) builder.setProgress(100, percent, false) else builder.setProgress(0, 0, true)
            nm.notify(NOTIF_ID, builder.build())
        }.onFailure { Log.w(TAG, "progress notification failed", it) }
    }

    private fun cancelProgressNotification(ctx: Context) {
        if (config?.showProgressNotification != true) return
        runCatching {
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            nm.cancel(NOTIF_ID)
        }
    }

    // -------------------------------------------------------------------------
    // Small helpers
    // -------------------------------------------------------------------------

    private fun newExecutor(name: String): ExecutorService =
        Executors.newSingleThreadExecutor { runnable -> Thread(runnable, name) }

    private fun openHttpConnection(
        url: String,
        connectTimeoutMs: Int = HTTP_TIMEOUT_MS,
        readTimeoutMs: Int = HTTP_TIMEOUT_MS
    ): HttpURLConnection {
        val conn = URL(url).openConnection() as HttpURLConnection
        conn.connectTimeout = connectTimeoutMs
        conn.readTimeout = readTimeoutMs
        conn.instanceFollowRedirects = true
        conn.useCaches = false
        conn.setRequestProperty("User-Agent", USER_AGENT)
        return conn
    }

    private fun readTextSafe(stream: InputStream?): String {
        if (stream == null) return ""
        return try {
            stream.bufferedReader().readText()
        } catch (t: Throwable) {
            ""
        }
    }

    private fun parseCheckResponse(body: String): CheckResult {
        if (body.isBlank()) return CheckResult(false, false, null)
        val root = JSONObject(body)
        val releaseObj = root.optJSONObject("release")
        val release = releaseObj?.let {
            ReleaseInfo(
                versionName = it.optString("versionName"),
                versionCode = it.optLong("versionCode"),
                sha256 = it.optString("sha256"),
                size = it.optLong("size"),
                releaseNotes = it.optString("releaseNotes"),
                downloadUrl = it.optString("downloadUrl"),
                directUrl = it.optString("directUrl"),
                minVersionCode = it.optLong("minVersionCode")
            )
        }
        return CheckResult(
            updateAvailable = root.optBoolean("updateAvailable", false),
            updateRequired = root.optBoolean("updateRequired", false),
            release = release
        )
    }

    private fun parseApiError(body: String): String = runCatching {
        JSONObject(body).optString("error")
    }.getOrDefault("")

    @Suppress("DEPRECATION") // getPackageInfo(String, int) is the portable form; longVersionCode is used on 28+
    private fun currentVersionCode(ctx: Context): Long = runCatching {
        val info = ctx.packageManager.getPackageInfo(ctx.packageName, 0)
        if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else info.versionCode.toLong()
    }.getOrDefault(0L)

    private fun isDeviceOwner(ctx: Context): Boolean = runCatching {
        val dpm = ctx.getSystemService(Context.DEVICE_POLICY_SERVICE) as? DevicePolicyManager
        dpm?.isDeviceOwnerApp(ctx.packageName) ?: false
    }.getOrDefault(false)

    private fun isMetered(ctx: Context): Boolean = runCatching {
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
        cm?.isActiveNetworkMetered ?: true
    }.getOrDefault(true)

    private fun downloadDir(ctx: Context): File =
        ctx.getExternalFilesDir(DOWNLOAD_DIR)
            ?: File(ctx.filesDir, DOWNLOAD_DIR).also { it.mkdirs() }

    private fun apkFileFor(ctx: Context, versionCode: Long): File =
        File(downloadDir(ctx), "$versionCode.apk")

    private fun digestStream(input: InputStream, digest: MessageDigest) {
        val buffer = ByteArray(64 * 1024)
        input.use { ins ->
            while (true) {
                val n = ins.read(buffer)
                if (n < 0) break
                digest.update(buffer, 0, n)
            }
        }
    }

    private fun sha256OfFile(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        digestStream(file.inputStream(), digest)
        return toHex(digest.digest())
    }

    private fun toHex(bytes: ByteArray): String {
        val sb = StringBuilder(bytes.size * 2)
        for (b in bytes) {
            val v = b.toInt() and 0xFF
            sb.append(HEX[v ushr 4]).append(HEX[v and 0x0F])
        }
        return sb.toString()
    }

    private fun extraIntent(intent: Intent, name: String): Intent? =
        if (Build.VERSION.SDK_INT >= 33) {
            intent.getParcelableExtra(name, Intent::class.java)
        } else {
            @Suppress("DEPRECATION")
            intent.getParcelableExtra(name)
        }

    private fun startActivityCompat(context: Context, intent: Intent) {
        val activity = activityRef?.get()
        if (activity != null && !activity.isFinishing) {
            runCatching { activity.startActivity(intent) }
                .onFailure { Log.w(TAG, "startActivity from activity failed", it) }
        } else {
            runCatching { context.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
                .onFailure { Log.w(TAG, "startActivity from context failed", it) }
        }
    }

    private fun emitState(state: UpdateState, detail: String?) {
        mainHandler.post {
            for (listener in listeners) {
                runCatching { listener(state, detail) }
                    .onFailure { Log.w(TAG, "state listener threw", it) }
            }
        }
    }

    /**
     * Tiny key/value store backed by a `java.util.Properties` file named
     * [PREFS_FILE] in app-private storage (via [Context.openFileOutput] /
     * [Context.openFileInput]). All access is synchronized; writes rewrite the
     * whole file. Used instead of SharedPreferences to stay dependency-light
     * and fully deterministic for tests.
     */
    private class ZenPrefs(private val context: Context) {

        private val lock = Any()
        private val props = Properties()
        private var loaded = false

        private fun ensureLoaded() {
            synchronized(lock) {
                if (loaded) return
                runCatching {
                    context.openFileInput(ZenUpdater.PREFS_FILE).use { input -> props.load(input) }
                }
                loaded = true
            }
        }

        fun get(key: String, def: String? = null): String? {
            ensureLoaded()
            synchronized(lock) { return props.getProperty(key) ?: def }
        }

        fun put(key: String, value: String?) {
            ensureLoaded()
            synchronized(lock) {
                if (value == null) props.remove(key) else props.setProperty(key, value)
                runCatching {
                    context.openFileOutput(ZenUpdater.PREFS_FILE, Context.MODE_PRIVATE).use { out ->
                        props.store(out, "Zen OTA state (managed by ZenUpdater; do not edit)")
                    }
                }.onFailure { Log.w(ZenUpdater.TAG, "failed to persist ${ZenUpdater.PREFS_FILE}", it) }
            }
        }

        fun getLong(key: String, def: Long = 0L): Long = get(key)?.toLongOrNull() ?: def

        fun putLong(key: String, value: Long) = put(key, value.toString())
    }
}

/**
 * A single release as served by `GET /api/check` (contract §2.1).
 * Kept `internal`; user code reads versionName/notes from [ZenUpdater.lastReleaseInfo]
 * or its own UI state.
 */
internal data class ReleaseInfo(
    val versionName: String,
    val versionCode: Long,
    val sha256: String,
    val size: Long,
    val releaseNotes: String,
    val downloadUrl: String,
    val directUrl: String,
    val minVersionCode: Long
)

/** Parsed `GET /api/check` response. */
internal data class CheckResult(
    val updateAvailable: Boolean,
    val updateRequired: Boolean,
    val release: ReleaseInfo?
)
