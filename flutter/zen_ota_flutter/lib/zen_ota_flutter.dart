// zen_ota_flutter.dart — Dart API for the Zen OTA Android updater.
//
// The platform side (ota.zen.flutter.ZenOtaPlugin) delegates everything to the
// zero-dependency Kotlin SDK (ota.zen.android.ZenUpdater), which performs the
// check against your Zen OTA API, downloads + SHA-256-verifies the APK and
// installs it (silent on device-owner devices, one-tap confirm otherwise).
//
// Minimal integration:
//
//   void main() {
//     WidgetsFlutterBinding.ensureInitialized();
//     ZenOta.init(const ZenConfig(
//       apiUrl: 'https://zen-ota-your-project.vercel.app',
//       appId: 'com.example.myapp',
//     ));
//     ZenOta.events.listen((e) => debugPrint('${e.state} ${e.detail ?? ''}'));
//     runApp(const MyApp());
//   }
//
// That's it. The plugin auto-checks on launch (when [ZenConfig.autoCheck] is
// true), downloads in the background and installs per [ZenConfig.installMode].

import 'dart:async';

import 'package:flutter/services.dart';

/// Method channel to the native plugin (ota.zen.flutter.ZenOtaPlugin).
const MethodChannel _zenMethod = MethodChannel('zen_ota');

/// Event channel delivering [ZenUpdateEvent]s from the native updater.
const EventChannel _zenEvents = EventChannel('zen_ota/events');

/// How the SDK installs a downloaded update.
enum ZenInstallMode {
  /// Silent install if the app is Device Owner, otherwise a one-tap
  /// user-confirmation prompt (default).
  auto,

  /// Never prompt: silent install (Device Owner only) or emit
  /// [ZenUpdateState.failed].
  silentOnly,

  /// Never install silently: always ask the user.
  promptOnly,
}

/// Lifecycle of an update. Mirrors the native `UpdateState` enum.
enum ZenUpdateState {
  /// Initial state.
  idle,

  /// A check request is in flight.
  checking,

  /// The server has a newer release.
  ///
  /// `detail == 'forced'` → the update is mandatory (running versionCode is
  /// below the release `minVersionCode`) and the hosting UI MUST show a
  /// non-dismissable blocking dialog.
  ///
  /// `detail == 'waiting for an unmetered network'` → the download was
  /// deferred because [ZenConfig.allowMetered] is false.
  available,

  /// The APK is downloading. `detail` is a percentage string like "42%".
  downloading,

  /// Download finished and SHA-256 verified. `detail` is the versionName.
  downloaded,

  /// The install was committed; the app process is about to be replaced.
  /// `detail` is the versionName.
  installing,

  /// No update available — running the newest release of the channel.
  upToDate,

  /// Something failed. `detail` carries a human-readable reason.
  failed,
}

/// Configuration for the updater. Mirrors the native `ZenConfig`.
class ZenConfig {
  const ZenConfig({
    required this.apiUrl,
    required this.appId,
    this.channel = 'stable',
    this.installMode = ZenInstallMode.auto,
    this.autoCheck = true,
    this.checkIntervalMs = 4 * 60 * 60 * 1000,
    this.allowMetered = true,
    this.showProgressNotification = false,
  });

  /// Base URL of your Zen OTA API deployment, e.g.
  /// `https://zen-ota-your-project.vercel.app` (no `/api` suffix needed).
  final String apiUrl;

  /// The applicationId of your Flutter app, e.g. `com.example.myapp`.
  /// Must match what you publish with the Zen CLI.
  final String appId;

  /// Release channel: `stable` (default), `beta` or `alpha`.
  final String channel;

  /// Install strategy, see [ZenInstallMode].
  final ZenInstallMode installMode;

  /// When true (default) the plugin checks on app launch, throttled by
  /// [checkIntervalMs].
  final bool autoCheck;

  /// Minimum interval between automatic checks (default 4 hours). Manual
  /// [ZenOta.check] calls always bypass this gate.
  final int checkIntervalMs;

  /// Whether downloads may run on metered networks (default true).
  final bool allowMetered;

  /// Show a native progress notification while downloading (default false).
  /// Requires POST_NOTIFICATIONS on Android 13+; skipped silently if ungranted.
  final bool showProgressNotification;

  Map<String, Object?> toJson() => <String, Object?>{
        'apiUrl': apiUrl,
        'appId': appId,
        'channel': channel,
        'installMode': installMode.name,
        'autoCheck': autoCheck,
        'checkIntervalMs': checkIntervalMs,
        'allowMetered': allowMetered,
        'showProgressNotification': showProgressNotification,
      };

  @override
  String toString() => 'ZenConfig(${toJson()})';
}

/// One state change of the update pipeline.
class ZenUpdateEvent {
  const ZenUpdateEvent(this.state, this.detail);

  final ZenUpdateState state;

  /// State-specific detail: `"forced"`, `"42%"`, a versionName, a failure
  /// reason, or null.
  final String? detail;

  @override
  String toString() => 'ZenUpdateEvent($state, $detail)';
}

/// Static facade over the native Zen updater.
abstract final class ZenOta {
  static bool _initialized = false;

  /// Broadcast stream of updater state changes.
  ///
  /// The native side starts forwarding events as soon as you listen; events
  /// are emitted on the Android main thread and delivered on the Dart main
  /// isolate. Listen as early as possible (in `main()`) so no state is missed.
  static Stream<ZenUpdateEvent> get events =>
      _zenEvents.receiveBroadcastStream().map((dynamic data) {
        final map = data as Map<Object?, Object?>;
        return ZenUpdateEvent(
          _parseState(map['state'] as String?),
          map['detail'] as String?,
        );
      });

  /// Initializes the native updater. Call once from `main()` before
  /// [runApp].
  ///
  /// ```dart
  /// WidgetsFlutterBinding.ensureInitialized();
  /// await ZenOta.init(const ZenConfig(
  ///   apiUrl: 'https://zen-ota-your-project.vercel.app',
  ///   appId: 'com.example.myapp',
  /// ));
  /// ```
  ///
  /// Throws [PlatformException] if the engine is not attached or the native
  /// configuration is rejected (blank apiUrl/appId).
  static Future<void> init(ZenConfig config) async {
    await _zenMethod.invokeMethod<void>('init', config.toJson());
    _initialized = true;
  }

  /// Manually checks for updates now, bypassing [ZenConfig.checkIntervalMs].
  /// A manual call also counts as the first check of the process, so the
  /// auto-check will not double-fire.
  static Future<void> check() async {
    await _zenMethod.invokeMethod<void>('check');
  }

  /// The stable per-install device id (random UUID, persisted in app-private
  /// storage). The server uses it for deterministic staged-rollout bucketing.
  static Future<String> deviceId() async =>
      await _zenMethod.invokeMethod<String>('deviceId') ?? '';

  /// The versionCode of the currently installed APK.
  static Future<int> versionCode() async =>
      await _zenMethod.invokeMethod<int>('versionCode') ?? 0;

  /// The versionName of the currently installed APK.
  static Future<String> versionName() async =>
      await _zenMethod.invokeMethod<String>('versionName') ?? '';

  /// True after a successful [init] in this process.
  static bool get initialized => _initialized;

  static ZenUpdateState _parseState(String? raw) =>
      ZenUpdateState.values.firstWhere(
        (s) => s.name == raw?.toLowerCase(),
        orElse: () => ZenUpdateState.failed,
      );
}
