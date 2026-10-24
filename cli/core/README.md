# Zen CLI — Go core

The publisher-side CLI of Zen OTA. It uploads APKs to GitHub Releases, commits
the release manifest (`.zen/manifest.json`) to the user's repository, mirrors
the manifest to the Zen serverless API and optionally to Cloudflare R2.
Implements `docs/API_CONTRACT.md` §1 (manifest), §5 (CLI ↔ API) and §6
(GitHub release conventions).

## Build

    cd cli/core
    go build -o zen .

Go 1.23+, standard library only — the build works fully offline.

## Cross-compile matrix

The npm wrapper shells out to a prebuilt binary; build them with:

    GOOS=linux   GOARCH=amd64 go build -o ../bin/zen-linux-amd64 .
    GOOS=linux   GOARCH=arm64 go build -o ../bin/zen-linux-arm64 .
    GOOS=darwin  GOARCH=amd64 go build -o ../bin/zen-darwin-amd64 .
    GOOS=darwin  GOARCH=arm64 go build -o ../bin/zen-darwin-arm64 .
    GOOS=windows GOARCH=amd64 go build -o ../bin/zen-windows-amd64.exe .

## Commands

| Command | Purpose |
|---|---|
| `zen login` | store GitHub token (+ API base URL + admin token) in `~/.zen/config.json` |
| `zen whoami` | show masked credentials and GitHub identity |
| `zen init` | create `zen.config.json` (git-remote + APK autodetect, interactive prompts, non-interactive flags) |
| `zen gen-dropin` | write the embedded `ZenUpdater.kt` drop-in into the current directory |
| `zen publish <apk>` | flagship: release + asset upload + manifest commit + admin mirror + optional R2 |
| `zen list` | per-channel release table (or `--json` raw manifest) |
| `zen rollback <version>` | move an older release to the front of a channel |
| `zen promote <version>` | copy a release from one channel to another (`--from/--to`) |
| `zen version` | print `zen version 1.0.0` |

Every command supports `--help`. Flags may appear after positional arguments
(the argument parser re-orders them for the stdlib `flag` package).

## Publishing flow (`zen publish`)

1. Load `zen.config.json` + `~/.zen/config.json`, resolve the APK path
   (a directory resolves to its newest `*.apk`).
2. Parse the APK with a pure-Go AXML reader: `package`, `versionCode`,
   `versionName`; `package` must equal the project's `appId`.
3. Validate channel whitelist and `--rollout` 0–100.
4. Fetch the current manifest: admin API → raw.githubusercontent.com →
   GitHub Contents API (private repos) → fresh manifest on 404.
5. Enforce versionCode monotonicity (contract §8); `--force-publish`
   (with confirmation or `--yes`) allows rollback-style releases.
6. `--dry-run` prints the plan and exits without writing anything.
7. Create/reuse the release `zen-<channel>-v<versionName>` (contract §6),
   delete the existing same-name asset, stream-upload the APK
   (`<prefix>-<channel>-v<versionName>.apk`).
8. Optional `--r2` mirror (SigV4 PUT) fills `assets.r2`.
9. Commit the pretty-printed manifest (create or update via Contents API)
   with message `zen: publish v<versionName> (code N) to <channel>`, then
   best-effort POST it to `/api/admin/manifest`.
10. Print a summary table + a smoke-test `curl` hint.

## Architecture

- `cmd/zen` — dispatcher, one file per command, stdlib `flag` parsing
- `internal/apk` — pure-Go binary AXML reader (string pool UTF-8/UTF-16,
  start-element chunks, 20-byte attributes) + streaming SHA-256
- `internal/manifest` — manifest types & history rules: newest-first,
  dedupe by versionCode, cap 25, `RollbackTo`, `Promote` (contract §1)
- `internal/github` — token client: releases, asset upload/delete,
  Contents API, 429 retry with Retry-After, 15s/60s timeouts
- `internal/api` — admin API client (`/api/admin/manifest` POST/GET, 5s)
- `internal/r2` — hand-rolled AWS SigV4 PUT for Cloudflare R2 (region auto)
- `internal/config` — `~/.zen/config.json` (chmod 600, atomic) + project
  `zen.config.json`
- `internal/atomicfile` — tmp + fsync + rename atomic writes
- `internal/ui` — ANSI colors (NO_COLOR + TTY aware), step/table printers,
  y/N confirm + default prompts
- `internal/assets` — embedded `ZenUpdater.kt` (placeholder, replaced at
  build time from `android/dropin/`)

## Config files

`~/.zen/config.json` (global, chmod 600): `githubToken`, `apiBaseUrl`,
`adminToken`, `r2AccountId`, `r2AccessKeyId`, `r2SecretAccessKey`,
`r2Bucket`, `r2PublicBase`.

`zen.config.json` (per project):

```json
{ "appId": "com.example.app", "repo": "owner/repo", "branch": "main",
  "apiBaseUrl": "https://...", "manifestPath": ".zen/manifest.json",
  "channels": ["stable", "beta", "alpha"], "apkAssetPrefix": "app" }
```

## Tests

    go test ./...
