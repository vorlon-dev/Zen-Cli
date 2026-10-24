# Zen OTA — Serverless API (Vercel)

Implementation of `../docs/API_CONTRACT.md` §2–§4. Plain Vercel Serverless
Functions (`api/*.ts`), **zero npm dependencies** — pure Node 20+ (global
`fetch`, Web streams). TypeScript strict mode.

## Layout

This directory is the Vercel project root. `api/*.ts` are the serverless
functions (route `/api/<name>`); `lib/` holds the shared, dependency-free code.

```
api/                        ← Vercel project root (deploy this directory)
├── package.json            zero runtime deps; typecheck script
├── tsconfig.json           strict, noEmit
├── vercel.json             function limits + clean-path rewrites
├── README.md
├── api/                    ← serverless functions (route /api/<name>)
│   ├── check.ts            GET  /api/check        §2.1 update check + staged rollout
│   ├── download.ts         GET  /api/download     §2.2 302 redirect or private proxy
│   ├── event.ts            POST /api/event        §2.3 install_success/install_failed
│   ├── stats.ts            GET  /api/stats        §2.4 admin usage stats
│   ├── admin-manifest.ts   POST+GET /api/admin/manifest  §2.5/§2.6 KV manifest push/read
│   └── health.ts           GET  /api/health       liveness probe
└── lib/
    ├── types.ts            contract interfaces + minimal local Vercel types
    ├── env.ts              ZEN_APPS / token access (ZEN_APPS parsed once, cached)
    ├── kv.ts               Upstash REST client over fetch (get/set/incr/mget/pipeline)
    ├── stats.ts            INCR counters + 90-day TTLs, day-range aggregation
    ├── manifest.ts         KV → raw.githubusercontent → Contents API sourcing
    ├── rollout.ts          sha256 bucketing (first 8 hex % 100)
    └── http.ts             CORS/JSON helpers, body parsing, admin gate (timing-safe)
```

`vercel.json` also maps the clean paths `/check`, `/download`, `/event`,
`/health` and the contract path `/api/admin/manifest` onto the functions.

## Deploy

```bash
npm install -g vercel
cd zen-ota/api          # this directory is the Vercel project root
vercel link             # create/select the project
vercel env add ZEN_APPS production
# …add the other env vars you need (see table below)
vercel --prod           # or: npm run deploy
```

`npm run dev` runs `vercel dev` locally; `npm run typecheck` runs
`tsc --noEmit` (CI gate — must be clean).

## Environment variables

| Variable | Required | Purpose |
|---|---|---|
| `ZEN_APPS` | yes | JSON map `appId → {repo, branch, manifestPath, private}`. Which repos serve which apps. |
| `GITHUB_TOKEN` | for private repos | Manifest reads (Contents API) + release-asset proxying. Fine-grained token with read access to the repos. |
| `ZEN_ADMIN_TOKEN` | for admin APIs | Bearer token for `/api/stats` and `/api/admin/manifest`. Unset → those endpoints answer `503 admin disabled`. |
| `KV_REST_API_URL` | optional | Upstash/Vercel KV REST endpoint. Enables stats + instant manifest propagation. |
| `KV_REST_API_TOKEN` | optional | Upstash/Vercel KV REST token. |

**KV is optional.** Without it the API still serves checks/downloads/events;
it just degrades gracefully: `/api/stats` → `501 {"error":"stats require KV"}`
and `POST /api/admin/manifest` → `501` (manifest propagation then relies on
the 60s edge cache + GitHub fetch). Nothing else changes.

### Pointing ZEN_APPS at a repo

`ZEN_APPS` is a single JSON string, e.g.:

```json
{
  "com.example.app": {
    "repo": "acme/counter-app",
    "branch": "main",
    "manifestPath": ".zen/manifest.json",
    "private": false
  },
  "com.acme.internal.app": {
    "repo": "acme/internal-app",
    "branch": "main",
    "manifestPath": ".zen/manifest.json",
    "private": true
  }
}
```

- `repo`: `owner/repo` on GitHub. The CLI publishes `.zen/manifest.json` there
  and attaches APKs to GitHub Releases (`zen-<channel>-v<versionName>` tags).
- `branch` / `manifestPath`: only needed if you deviate from `main` /
  `.zen/manifest.json`.
- `private: true`: manifest is read via the GitHub Contents API with
  `GITHUB_TOKEN`, `/api/check` hides `directUrl`, and `/api/download` streams
  the release asset through the function instead of redirecting.

## Testing after deploy

```bash
BASE=https://<your-deployment>.vercel.app

# 1. Liveness
curl -s "$BASE/api/health"

# 2. Update check (public repo, default channel)
curl -s "$BASE/api/check?appId=com.example.app&versionCode=23&deviceId=11111111-2222-3333-4444-555555555555"
# → {"appId":"com.example.app","channel":"stable","updateAvailable":true,…,"release":{…,"downloadUrl":"…/api/download?…","directUrl":"https://github.com/…/app-stable-v1.2.0.apk"}}

# 3. Follow the download (302 for public repos)
curl -sI "$BASE/api/download?appId=com.example.app&channel=stable&versionCode=24" | head -n 5

# 4. Report an install
curl -s -o /dev/null -w "%{http_code}\n" -X POST "$BASE/api/event" \
  -H 'Content-Type: application/json' \
  -d '{"appId":"com.example.app","channel":"stable","versionCode":24,"type":"install_success"}'

# 5. Admin stats (needs ZEN_ADMIN_TOKEN + KV)
curl -s -H "Authorization: Bearer $ZEN_ADMIN_TOKEN" \
  "$BASE/api/stats?appId=com.example.app&days=7"

# 6. Push a manifest to KV for instant propagation (needs admin token + KV)
curl -s -X POST "$BASE/api/admin/manifest" \
  -H "Authorization: Bearer $ZEN_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  --data-binary @.zen/manifest.json
```

Status codes worth remembering: `400` bad/missing params, `404 unknown appId`
/ `unknown channel` / `release not found`, `401` bad admin token, `403` never
(we answer 401), `503 admin disabled` when `ZEN_ADMIN_TOKEN` is unset, `501`
when an admin endpoint needs KV, `500 {"error":"internal"}` catch-all.
