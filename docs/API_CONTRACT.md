# Zen OTA — API Contract v1

This document is the **single source of truth** shared by the Zen CLI (Go), the serverless
API (Vercel), and the Android SDK (Kotlin). Any change must be reflected here first.

---

## 1. Release Manifest

The manifest lives in the user's GitHub repository at `.zen/manifest.json` (source of truth)
and is optionally mirrored into Vercel KV for instant propagation.

```json
{
  "schemaVersion": 1,
  "appId": "com.example.app",
  "updatedAt": "2026-10-23T10:00:00Z",
  "channels": {
    "stable": { "history": [ZenRelease, ...] },
    "beta":   { "history": [ZenRelease, ...] },
    "alpha":  { "history": [ZenRelease, ...] }
  }
}
```

Rules:
- `history` is ordered **newest first**. `history[0]` = current release, `history[1]` =
  previous release (used by staged rollout). Cap length at 25 entries.
- A channel object that has never had a publish simply does not exist in `channels`.

### ZenRelease

```json
{
  "versionName": "1.2.0",
  "versionCode": 24,
  "sha256": "<hex sha256 of the apk file>",
  "size": 12345678,
  "releaseNotes": "plain text or markdown",
  "publishedAt": "2026-10-23T10:00:00Z",
  "rollout": 100,
  "minVersionCode": 0,
  "assets": {
    "github": "https://github.com/OWNER/REPO/releases/download/TAG/ASSET.apk",
    "r2": "https://... | null"
  }
}
```

Rules:
- `rollout` is 0–100. `100` = fully rolled out (default).
- `minVersionCode`: devices running a `versionCode` **below** this value MUST update
  (`updateRequired: true`). `0` = no forced floor.
- `assets.github` is always present. `assets.r2` is optional.

---

## 2. Public API Endpoints (Vercel)

Base URL: `https://<deployment>/api`

### 2.1 `GET /api/check`

Query params:
| Param | Required | Description |
|---|---|---|
| `appId` | yes | application id, e.g. `com.example.app` |
| `versionCode` | yes | integer, the device's current version code |
| `channel` | no | `stable` (default) / `beta` / `alpha` |
| `deviceId` | yes* | stable random UUID generated once per install (*required for rollout correctness; if missing the API treats device as "in group") |
| `platform` | no | `android` (default) |

Resolution logic (server):
1. Load manifest (KV if configured, else GitHub raw/Contents API).
2. `ch = manifest.channels[params.channel]`. If missing → `404 { "error": "unknown channel" }`.
3. `current = ch.history[0]`, `previous = ch.history[1] || null`.
4. If `current` missing → `200` with `updateAvailable:false` and no `release`.
5. Rollout: if `current.rollout >= 100` → serve `current`.
   Else compute `bucket = sha256_hex(appId + ":" + channel + ":" + deviceId + ":" + current.versionCode)` — take the **first 8 hex chars as an integer**, `bucket % 100 < current.rollout` → in group.
   - In group → serve `current`.
   - Not in group → serve `previous` if `previous != null && previous.versionCode > versionCode`; else no update (`release: null`).
6. Serve decision:
   - `updateAvailable = (served.versionCode > params.versionCode)`
   - `updateRequired = (params.versionCode < served.minVersionCode)`
   - If `updateAvailable == false` → `release: null` but still 200.
7. Record a `check` stat event (best-effort, must never block or fail the request).

Response `200`:
```json
{
  "appId": "com.example.app",
  "channel": "stable",
  "updateAvailable": true,
  "updateRequired": false,
  "release": {
    "versionName": "1.2.0",
    "versionCode": 24,
    "sha256": "...",
    "size": 12345678,
    "releaseNotes": "...",
    "publishedAt": "2026-10-23T10:00:00Z",
    "rollout": 100,
    "minVersionCode": 0,
    "downloadUrl": "https://<base>/api/download?appId=com.example.app&channel=stable&versionCode=24",
    "directUrl": "https://github.com/OWNER/REPO/releases/download/TAG/ASSET.apk"
  },
  "serverTime": "2026-10-23T12:00:00Z"
}
```

- `downloadUrl` ALWAYS points at `/api/download` (enables stats + private-repo proxying).
- `directUrl` = `assets.r2 || assets.github`, exposed for public repos fast-path.
- For private repos `directUrl` MUST be `null`.

Errors: `400` missing/invalid params (`{"error": "..."}`), `404` unknown appId or channel
(`{"error": "unknown appId"}`), `500` upstream failures. All JSON.

### 2.2 `GET /api/download`

Query params: `appId` (req), `channel` (default stable), `versionCode` (req), `deviceId` (opt).

Resolution: manifest → channel history → find entry with matching `versionCode`.
If not found → `404 {"error":"release not found"}`.

- **Public repo**: respond `302` with `Location` header → `assets.r2 || assets.github`.
- **Private repo** (`ZEN_APPS[].private == true`): stream the GitHub release asset through
  the function using `GITHUB_TOKEN` (Accept: `application/octet-stream` on the asset API),
  passing through `Content-Length` and `Content-Type: application/vnd.android.package-archive`.
- Record `download` stat event (best-effort) for both paths.

### 2.3 `POST /api/event`

Body JSON:
```json
{
  "appId": "com.example.app",
  "channel": "stable",
  "deviceId": "uuid",
  "versionCode": 24,
  "type": "install_success" | "install_failed",
  "detail": "optional free-form string"
}
```
Response `204`. Records `installs` or `install_failures` stat. Invalid body → `400`.

### 2.4 `GET /api/stats`  (admin)

Query: `appId` (req), `days` (default 7, max 90).
Header: `Authorization: Bearer <ZEN_ADMIN_TOKEN>`.
Response `200`:
```json
{
  "appId": "com.example.app",
  "days": 7,
  "series": [ { "date": "2026-10-23", "checks": 120, "downloads": 45, "installs": 31, "installFailures": 2 } ],
  "totals": { "checks": 800, "downloads": 300, "installs": 210, "installFailures": 9 }
}
```
- If KV is not configured → `501 {"error":"stats require KV"}`.
- If `ZEN_ADMIN_TOKEN` unset → `503 {"error":"admin disabled"}`. Bad/missing token → `401`.

### 2.5 `POST /api/admin/manifest`  (admin)

Header: `Authorization: Bearer <ZEN_ADMIN_TOKEN>`. Body: full manifest JSON (validated:
`schemaVersion`, `appId`, `channels` present; each channel has non-empty `history`).
Writes to KV under `zen:manifest:<appId>`. Response `200 {"ok":true}`.
- KV unconfigured → `501`. `ZEN_ADMIN_TOKEN` unset → `503`. Bad token → `401`.

### 2.6 `GET /api/admin/manifest`  (admin)

Query: `appId`. Returns the resolved manifest (KV first, else repo source) with
`200 {"source":"kv"|"github","manifest":{...}}`. Same auth behavior as 2.5.

---

## 3. Manifest Sourcing (API side)

`ZEN_APPS` env var — JSON map:
```json
{ "com.example.app": { "repo": "owner/repo", "branch": "main", "manifestPath": ".zen/manifest.json", "private": false } }
```

`getManifest(appId)`:
1. If KV configured → try `zen:manifest:<appId>`, return with `source:"kv"` on hit.
2. Public repo → `https://raw.githubusercontent.com/<repo>/<branch>/<manifestPath>`.
3. Private repo → GitHub Contents API `https://api.github.com/repos/<repo>/contents/<path>?ref=<branch>`
   with `GITHUB_TOKEN`, base64-decode `content`.
4. Wrap responses with edge caching headers: `Cache-Control: s-maxage=60, stale-while-revalidate=300`.

## 4. Stats Storage (optional KV, Upstash REST)

When `KV_REST_API_URL` + `KV_REST_API_TOKEN` are set (Vercel KV provides these), use the
Upstash REST API directly over fetch (NO npm dependency):
- Key: `zen:stats:<appId>:<YYYY-MM-DD>` holding a JSON object
  `{"checks":n,"downloads":n,"installs":n,"installFailures":n}`
- Increment: `POST /kv/hincrby`-style REST call per field is fine; simpler: read-modify-write
  with `INCR` on composite keys `zen:stats:<appId>:<date>:<field>` and aggregate in /api/stats
  (MGET over the date range). Prefer the composite-key `INCR` approach.

## 5. CLI ↔ API

- `zen publish` writes the manifest to GitHub (Contents API commit on `.zen/manifest.json`)
  and, when an admin token is configured, POSTs the same manifest to `/api/admin/manifest`
  for instant propagation. Neither failure of the admin POST is fatal (warn only).
- `zen list` / `zen rollback` / `zen promote` read via `/api/admin/manifest` when a token is
  present, else fall back to raw GitHub URL derived from `zen.config.json`.
- CLI config file `~/.zen/config.json` (chmod 600): `{"githubToken","apiBaseUrl","adminToken"}`.
- Project file `zen.config.json`:
  ```json
  { "appId": "com.example.app", "repo": "owner/repo", "branch": "main",
    "apiBaseUrl": "https://...", "manifestPath": ".zen/manifest.json",
    "channels": ["stable","beta","alpha"], "apkAssetPrefix": "app" }
  ```

## 6. GitHub Release conventions (CLI side)

- Release tag: `zen-<channel>-v<versionName>` (e.g. `zen-stable-v1.2.0`).
- Release title: `Zen <channel> v<versionName>`.
- Asset name: `<apkAssetPrefix>-<channel>-v<versionName>.apk` (default prefix `app`).
- Re-publishing the same version replaces the asset (delete then re-upload) and bumps the
  manifest entry.
- Optional R2 mirror: `zen publish --r2` uploads to R2 (creds from `~/.zen/config.json`
  fields `r2AccountId`, `r2AccessKeyId`, `r2SecretAccessKey`, `r2Bucket`, `r2PublicBase`)
  and fills `assets.r2`.

## 7. Android SDK behavior

- Config (code or `zen` block): `apiUrl`, `appId`, `channel` (default stable), optional
  `deviceId` (auto-generated + persisted if absent).
- Check on first Activity resume after app start (via `Application.ActivityLifecycleCallbacks`,
  zero androidx dependency) or manual `ZenUpdater.check()`.
- On `updateAvailable` → background download (HTTP, resume via `Range` header into
  `context.getExternalFilesDir("zen-ota")/<versionCode>.apk.part`), then SHA-256 verify
  (`release.sha256`; skip if empty but warn).
- Install strategy chain:
  1. **Silent**: if app is Device Owner → `PackageInstaller` session with
     `setRequireUserAction(false)` (API 31+; on older versions device-owner sessions commit
     silently), then `commit(intentSender)`. App process dies and restarts on the new build.
  2. **Prompt**: API 24+ `PackageInstaller` session with user confirmation, else
  3. **Intent**: `ACTION_INSTALL_PACKAGE` / `ACTION_VIEW` with `FileProvider` (needs
     `REQUEST_INSTALL_PACKAGES` on API 26+).
- After successful install detection (next open with matching versionCode) → POST `/api/event`
  `install_success`; on `Exception` during install → `install_failed`.
- `updateRequired == true` → the prompt UI must be non-dismissable (documented contract).

---

## 8. Version comparison rules

- `versionCode` is the sole ordering authority (integer, monotonic).
- `versionName` is display-only.
- Publishing a `versionCode` **lower than** the channel's `history[0].versionCode` is
  rejected unless `--force-publish` is passed (creates a rollback-style entry).
