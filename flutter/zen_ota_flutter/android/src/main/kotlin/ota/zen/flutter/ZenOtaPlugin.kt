/*
 * ZenOtaPlugin.kt — Flutter bridge for the Zen OTA Android SDK.
 *
 * Forwards the Dart MethodChannel calls to the zero-dependency Kotlin SDK
 * (ota.zen.android.ZenUpdater) and streams its UpdateState events back over
 * an EventChannel. The SDK file (ota.zen.android.ZenUpdater) ships verbatim
 * inside this plugin — single source of truth with the drop-in / AAR builds.
 */
package ota.zen.flutter

import android.app.Activity
import android.app.Application
import android.content.Context
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import io.flutter.embedding.engine.plugins.FlutterPlugin
import io.flutter.embedding.engine.plugins.activity.ActivityAware
import io.flutter.embedding.engine.plugins.activity.ActivityPluginBinding
import io.flutter.plugin.common.EventChannel
import io.flutter.plugin.common.MethodCall
import io.flutter.plugin.common.MethodChannel
import ota.zen.android.InstallMode
import ota.zen.android.UpdateState
import ota.zen.android.ZenConfig
import ota.zen.android.ZenUpdater

private const val METHOD_CHANNEL = "zen_ota"
private const val EVENT_CHANNEL = "zen_ota/events"
private const val DEFAULT_CHECK_INTERVAL_MS = 4L * 60 * 60 * 1000

class ZenOtaPlugin : FlutterPlugin, MethodChannel.MethodCallHandler, EventChannel.StreamHandler, ActivityAware {

    private var applicationContext: Context? = null
    private var methodChannel: MethodChannel? = null
    private var eventChannel: EventChannel? = null

    /** Current activity (strongly held only while attached — Flutter holds it anyway). */
    private var activity: Activity? = null

    /**
     * True once ANY activity has resumed since the plugin attached. Flutter's
     * Dart entrypoint usually starts before the first onResume, so the SDK's
     * own ActivityLifecycleCallbacks catch the first resume and fire the
     * auto-check. When init() arrives AFTER a resume already happened (racy
     * embeddings, hot restart), the SDK would never see a fresh resume — this
     * flag lets the plugin fire the launch check itself.
     */
    private var activityResumedSinceAttach = false

    /** EventSink must only be touched on the main thread. */
    private var sink: EventChannel.EventSink? = null
    private val mainHandler = Handler(Looper.getMainLooper())

    private val stateListener: (UpdateState, String?) -> Unit = { state, detail ->
        mainHandler.post {
            // The SDK already emits on the main thread; the post is a cheap
            // guarantee against future internal changes.
            sink?.success(mapOf("state" to state.name, "detail" to detail))
        }
    }

    // ------------------------------------------------------------------ //
    // FlutterPlugin
    // ------------------------------------------------------------------ //

    override fun onAttachedToEngine(binding: FlutterPlugin.FlutterPluginBinding) {
        applicationContext = binding.applicationContext
        methodChannel = MethodChannel(binding.binaryMessenger, METHOD_CHANNEL)
            .also { it.setMethodCallHandler(this) }
        eventChannel = EventChannel(binding.binaryMessenger, EVENT_CHANNEL)
            .also { it.setStreamHandler(this) }
    }

    override fun onDetachedFromEngine(binding: FlutterPlugin.FlutterPluginBinding) {
        methodChannel?.setMethodCallHandler(null)
        methodChannel = null
        eventChannel?.setStreamHandler(null)
        eventChannel = null
        applicationContext = null
        sink = null
    }

    // ------------------------------------------------------------------ //
    // EventChannel.StreamHandler
    // ------------------------------------------------------------------ //

    override fun onListen(arguments: Any?, events: EventChannel.EventSink?) {
        sink = events
        ZenUpdater.addListener(stateListener)
    }

    override fun onCancel(arguments: Any?) {
        ZenUpdater.removeListener(stateListener)
        sink = null
    }

    // ------------------------------------------------------------------ //
    // MethodChannel.MethodCallHandler
    // ------------------------------------------------------------------ //

    override fun onMethodCall(call: MethodCall, result: MethodChannel.Result) {
        val ctx = applicationContext
        if (ctx == null) {
            result.error("unavailable", "ZenOtaPlugin is not attached to an engine", null)
            return
        }
        when (call.method) {
            "init" -> handleInit(call, ctx, result)
            "check" -> {
                ZenUpdater.check(activity)
                result.success(true)
            }
            "deviceId" -> result.success(ZenUpdater.deviceId(ctx))
            "versionCode" -> result.success(currentVersionCode(ctx))
            "versionName" -> result.success(currentVersionName(ctx))
            else -> result.notImplemented()
        }
    }

    private fun handleInit(call: MethodCall, ctx: Context, result: MethodChannel.Result) {
        val application = ctx as? Application
        if (application == null) {
            result.error(
                "unavailable",
                "Plugin applicationContext is not an Application instance; " +
                    "cannot register lifecycle callbacks",
                null,
            )
            return
        }

        val args = call.arguments<Map<*, *>>() ?: emptyMap<Any, Any>()
        val config = ZenConfig(
            apiUrl = args["apiUrl"] as? String ?: "",
            appId = args["appId"] as? String ?: "",
            channel = args["channel"] as? String ?: "stable",
            installMode = parseInstallMode(args["installMode"]),
            autoCheck = args["autoCheck"] as? Boolean ?: true,
            checkIntervalMs = (args["checkIntervalMs"] as? Number)?.toLong()
                ?: DEFAULT_CHECK_INTERVAL_MS,
            allowMetered = args["allowMetered"] as? Boolean ?: true,
            showProgressNotification = args["showProgressNotification"] as? Boolean ?: false,
        )

        try {
            ZenUpdater.init(application, config)
        } catch (e: Exception) {
            result.error("init_failed", e.message ?: "ZenUpdater.init failed", null)
            return
        }

        // Cover the race where the activity resumed before init() arrived:
        // fire the launch check ourselves, mirroring what the SDK's lifecycle
        // callback would have done. A manual check() counts as the first
        // process check, so no duplicate fires later.
        if (config.autoCheck && activityResumedSinceAttach) {
            ZenUpdater.check(activity)
        }
        result.success(true)
    }

    private fun parseInstallMode(raw: Any?): InstallMode = when (raw) {
        "silentOnly" -> InstallMode.SILENT_ONLY
        "promptOnly" -> InstallMode.PROMPT_ONLY
        else -> InstallMode.AUTO
    }

    private fun currentVersionCode(ctx: Context): Long = try {
        val pm = ctx.packageManager
        val packageName = ctx.packageName
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            pm.getPackageInfo(packageName, 0).longVersionCode
        } else {
            @Suppress("DEPRECATION")
            pm.getPackageInfo(packageName, 0).versionCode.toLong()
        }
    } catch (e: Exception) {
        0L
    }

    private fun currentVersionName(ctx: Context): String = try {
        ctx.packageManager.getPackageInfo(ctx.packageName, 0).versionName ?: ""
    } catch (e: Exception) {
        ""
    }

    // ------------------------------------------------------------------ //
    // ActivityAware
    // ------------------------------------------------------------------ //

    override fun onAttachedToActivity(binding: ActivityPluginBinding) {
        activity = binding.activity
        binding.addLifecycleListener(lifecycleListener)
    }
    override fun onDetachedFromActivityForConfigChanges() {
        activity = null
    }

    override fun onReattachedToActivityForConfigChanges(binding: ActivityPluginBinding) {
        activity = binding.activity
        binding.addLifecycleListener(lifecycleListener)
    }

    override fun onDetachedFromActivity() {
        activity = null
    }

    private val lifecycleListener = object : Application.ActivityLifecycleCallbacks {
        override fun onActivityResumed(activity: Activity) {
            activityResumedSinceAttach = true
        }

        override fun onActivityCreated(activity: Activity) {}

        override fun onActivityStarted(activity: Activity) {}

        override fun onActivityPaused(activity: Activity) {}

        override fun onActivityStopped(activity: Activity) {}

        override fun onSaveInstanceState(activity: Activity, outState: Bundle) {}

        override fun onActivityDestroyed(activity: Activity) {}
    }
}
