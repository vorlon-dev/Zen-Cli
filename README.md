# Zen OTA — In-app auto-updates for Android, backed by your own GitHub repo

**Zen** is a complete OTA (over-the-air) update system for Android apps that are NOT
distributed through the Play Store. You build an APK, run one command, and every user's
app updates itself — no repo visits, no manual downloads, no uninstall/reinstall.

```
 you                          your infra                      your users
┌─────────┐   zen publish    ┌──────────────────┐   GET /api/check   ┌─────────────┐
│ APK out  ├─────────────────►│  GitHub Releases ├◄───────────────────┤  Android app │
│ of CI/AS │   manifest commit│  (.zen/manifest) │   new version?     │ + ZenUpdater │
└─────────┘                   └────────┬─────────┘                    └──────┬──────┘
                                       │                     302 / stream    │
                                       ▼                                     ▼
                              ┌──────────────────┐                  background download
                              │  Vercel API      ├─────────────────► SHA-256 verify
                              │  check/download/ │                  silent install
                              │  stats/admin     │                  (device owner)
                              └──────────────────┘                  reopen → new build
```

## What's in the box

| Component | Path | Tech |
|---|---|---|
| **Zen CLI** (Go core) | `cli/core/` | Go 1.23, stdlib-only, ~3.2k lines |
| **Zen CLI** (npm wrapper) | `cli/npm/` | esbuild-style dispatcher + SHA-256 verified downloads |
| **Serverless API** | `api/` | Vercel functions, TypeScript, zero npm deps |
| **Android SDK** | `android/dropin/ZenUpdater.kt` + `android/zen-android/` | Kotlin, zero dependencies, 1.3k lines |
| **Flutter plugin** | `flutter/zen_ota_flutter/` | Dart API + platform-channel bridge over the same Kotlin SDK |
| **Example app wiring** | `example/` | copy-paste snippets |
| **Release automation** | `.github/workflows/release-cli.yml` | tag → 5-platform binaries → release + checksums |
| **Binding spec** | `docs/API_CONTRACT.md` | the contract all three sides implement |

## Feature matrix

- ✅ **Channels** — `stable`, `beta`, `alpha`, fully independent histories
- ✅ **Staged rollout** — `zen publish --rollout 10` serves the new build to a deterministic 10% of devices (the rest keep the previous release)
- ✅ **Force update** — `--force` / `--min-version-code`: SDK flags `updateRequired`, UI contract says non-dismissable
- ✅ **Rollback** — `zen rollback 1.2.1` flips the pointer in seconds, users get the old build on next open
- ✅ **Promote** — `zen promote 1.2.0 --from beta --to stable`
- ✅ **Release notes** — shown by the SDK / available in every check response
- ✅ **Stats** — checks, downloads, installs, install failures per day (`zen`-agnostic, `GET /api/stats`)
- ✅ **Silent install** — device-owner phones update with zero taps; everyone else gets a 1-tap confirm
- ✅ **Resumable + verified downloads** — `Range`-based resume, SHA-256 checked before install
- ✅ **Private-repo ready** — API proxies release assets with a server-side token; public repos redirect directly
- ✅ **R2 mirror optional** — `zen publish --r2`

## Quickstart (10 minutes)

### 1. Deploy the API

```bash
cd api
npm install -g vercel
vercel link

# single-app setup (JSON map, one line):
vercel env add ZEN_APPS production
# {"com.example.myapp": {"repo": "YOUR_USERNAME/your-app-repo", "branch": "main", "manifestPath": ".zen/manifest.json", "private": false}}

vercel env add ZEN_ADMIN_TOKEN production   # random string — CLI uses it for instant propagation + stats
vercel --prod
```

Full env-var reference: `api/README.md`. Stats + instant manifest propagation need a
Vercel KV (Upstash) store — optional, everything else works without it.

### 2. Install the CLI

```bash
npm install -g ./cli/npm        # from this repo
# or after you've pushed + tagged: npm install -g zen-cli
zen login                       # GitHub token (repo scope) + API base URL + admin token
```

### 3. Wire the app

**Native Android** — copy `android/dropin/ZenUpdater.kt` into your app (or add the
`zen-android` module), then in your `Application`:

```kotlin
ZenUpdater.init(this, ZenConfig(
    apiUrl = "https://zen-ota-your-project.vercel.app",
    appId  = "com.example.myapp",
))
```

**Flutter** — add the plugin, then in `main()`:

```dart
WidgetsFlutterBinding.ensureInitialized();
ZenOta.init(const ZenConfig(
  apiUrl: 'https://zen-ota-your-project.vercel.app',
  appId: 'com.example.myapp',
));
ZenOta.events.listen((e) => debugPrint('${e.state} ${e.detail ?? ""}'));
runApp(const MyApp());
```

Details: `flutter/zen_ota_flutter/README.md`.

Add permissions: `INTERNET` + `REQUEST_INSTALL_PACKAGES`. For fully silent installs,
provision devices as device owner (see `example/README.md`).

### 4. Ship an update

```bash
zen publish app-release.apk --notes "First OTA release"
```

Users open the app → update downloads in the background → installs → next open is the
new version. **Open, close, open — updated.**

## CLI reference

```
zen login        store GitHub + API credentials (~/.zen/config.json, chmod 600)
zen whoami       show stored credentials and GitHub identity
zen init         create zen.config.json for the current project
zen gen-dropin   write the current ZenUpdater.kt into the current directory
zen publish      publish an APK to a channel
  --channel      stable | beta | alpha            (default stable)
  --rollout N    staged rollout percentage        (default 100)
  --force        force update floor at this version
  --min-version-code N   explicit floor (devices below MUST update)
  --notes "..." / --notes-file F         release notes
  --r2           also mirror the APK to Cloudflare R2
  --dry-run      show the plan without touching anything
  --force-publish        allow publishing an older versionCode
zen list         table of releases per channel (--json supported)
zen rollback     point a channel back at an older release
zen promote      copy a release between channels (beta → stable)
```

## API endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/check?appId=&versionCode=&channel=&deviceId=` | update decision (rollout-aware, force-aware) |
| `GET /api/download?appId=&channel=&versionCode=` | 302 redirect (public) or tokened stream (private) |
| `POST /api/event` | `install_success` / `install_failed` telemetry |
| `GET /api/stats?appId=&days=7` | daily counters (admin) |
| `GET/POST /api/admin/manifest` | inspect / push manifest (admin) |
| `GET /api/health` | liveness |

## Security model

- APKs are SHA-256 hashed at publish time; the SDK verifies before installing.
- Manifest writes require your GitHub token; admin API surface requires `ZEN_ADMIN_TOKEN`.
- The npm wrapper verifies downloaded CLI binaries against `checksums.json`.
- Downloads for private repos are proxied server-side; tokens never reach clients.
- No user data collected: only appId, channel, a random per-install UUID, and counters.

## Versioning

Manifest `schemaVersion: 1`. CLI, API, and SDK ship together; the contract lives in
`docs/API_CONTRACT.md` — change it there first.

## Repo layout

```
zen-ota/
├── cli/
│   ├── core/          Go source (module github.com/zen-ota/zen-cli/core)
│   ├── dist/          prebuilt release archives (v1.0.0)
│   └── npm/           npm wrapper (name: zen-cli)
├── api/               Vercel serverless functions
├── android/
│   ├── dropin/        single-file SDK (copy into any app)
│   └── zen-android/   library module (same code, AAR-ready)
├── flutter/
│   └── zen_ota_flutter/   Flutter plugin (Dart API + platform channels)
├── example/           host-app integration guide
├── docs/API_CONTRACT.md
└── .github/workflows/release-cli.yml
```
