/**
 * lib/env.ts — typed environment access for the Zen OTA API.
 *
 * ZEN_APPS is a JSON map: appId -> { repo, branch, manifestPath, private }
 *   e.g. {"com.example.app":{"repo":"owner/repo","branch":"main",
 *          "manifestPath":".zen/manifest.json","private":false}}
 * It is parsed ONCE and cached in module scope (env vars are static per
 * deployment in Vercel's Node runtime).
 */

export interface ZenAppConfig {
  /** "owner/repo" */
  repo: string;
  /** defaults to "main" */
  branch: string;
  /** defaults to ".zen/manifest.json" */
  manifestPath: string;
  /** true = fetch manifest via Contents API + proxy release downloads */
  private: boolean;
}

interface EnvCache {
  apps: { [appId: string]: ZenAppConfig };
  /** true when ZEN_APPS was present but could not be parsed (warned once) */
  parseFailed: boolean;
}

let cache: EnvCache | undefined;

function loadApps(): EnvCache {
  if (cache) return cache;
  cache = { apps: {}, parseFailed: false };
  const raw = process.env.ZEN_APPS;
  if (!raw || raw.trim() === "") return cache;
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      cache.parseFailed = true;
      console.warn("zen-ota: ZEN_APPS must be a JSON object mapping appId -> config");
      return cache;
    }
    const apps: { [appId: string]: ZenAppConfig } = {};
    for (const [appId, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (!value || typeof value !== "object" || Array.isArray(value)) continue;
      const v = value as Record<string, unknown>;
      if (typeof v.repo !== "string" || v.repo.trim() === "") continue;
      apps[appId] = {
        repo: v.repo.trim(),
        branch: typeof v.branch === "string" && v.branch.trim() !== "" ? v.branch.trim() : "main",
        manifestPath:
          typeof v.manifestPath === "string" && v.manifestPath.trim() !== ""
            ? v.manifestPath.trim()
            : ".zen/manifest.json",
        private: v.private === true,
      };
    }
    cache = { apps, parseFailed: false };
  } catch (e) {
    cache = { apps: {}, parseFailed: true };
    console.warn("zen-ota: failed to parse ZEN_APPS as JSON", e);
  }
  return cache;
}

/** Returns the app config for appId, or null when unknown. */
export function getAppConfig(appId: string): ZenAppConfig | null {
  const apps = loadApps().apps;
  return Object.prototype.hasOwnProperty.call(apps, appId) ? apps[appId] : null;
}

/** True when ZEN_APPS is set and parsed into at least a valid (possibly empty) object. */
export function hasZenAppsConfig(): boolean {
  return process.env.ZEN_APPS !== undefined && !loadApps().parseFailed;
}

export function getGithubToken(): string {
  return process.env.GITHUB_TOKEN || "";
}

export function getAdminToken(): string {
  return process.env.ZEN_ADMIN_TOKEN || "";
}

/** Upstash REST credentials (Vercel KV exposes these env vars). */
export interface KvRestConfig {
  url: string;
  token: string;
}

export function getKvRestConfig(): KvRestConfig | null {
  const url = process.env.KV_REST_API_URL;
  const token = process.env.KV_REST_API_TOKEN;
  if (!url || !token) return null;
  return { url: url.replace(/\/+$/, ""), token };
}
