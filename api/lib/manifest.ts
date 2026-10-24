/**
 * lib/manifest.ts — manifest sourcing per API_CONTRACT.md §3.
 *
 * getManifest(appId):
 *   1. KV first (key `zen:manifest:{appId}`) → source "kv".
 *   2. Public repo → https://raw.githubusercontent.com/<repo>/<branch>/<manifestPath>.
 *   3. Private repo → GitHub Contents API (base64 `content`) with GITHUB_TOKEN.
 *   Otherwise → { source: "none", manifest: null, reason }.
 *
 * All outbound fetches have a 30s AbortController timeout.
 */

import { getAppConfig, getGithubToken } from "./env";
import { isKvConfigured, kvGet } from "./kv";
import type { ManifestSource, ZenManifest } from "./types";

/** Advise Vercel's edge network to cache responses for 60s + SWR 5min. */
export const EDGE_CACHE_HEADER: Record<string, string> = {
  "Cache-Control": "s-maxage=60, stale-while-revalidate=300",
};

const FETCH_TIMEOUT_MS = 30_000;
const GITHUB_API_BASE = "https://api.github.com";
const USER_AGENT = "zen-ota-api";

export interface ManifestResult {
  source: ManifestSource;
  manifest: ZenManifest | null;
  /** Set when source === "none": "unknown-app" (not in ZEN_APPS, no KV copy)
   *  or "unavailable" (app known but upstream fetch/parse failed). */
  reason?: "unknown-app" | "unavailable";
}

function fetchWithTimeout(url: string, init?: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);
  return fetch(url, { ...init, signal: controller.signal }).finally(() => clearTimeout(timer));
}

/** Structural sanity check so malformed JSON never crashes a handler. */
function parseManifest(raw: string): ZenManifest | null {
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return null;
    const m = parsed as Record<string, unknown>;
    if (typeof m.appId !== "string" || m.appId === "") return null;
    if (!m.channels || typeof m.channels !== "object" || Array.isArray(m.channels)) return null;
    return parsed as ZenManifest;
  } catch {
    return null;
  }
}

function encodePathSegments(path: string): string {
  return path
    .replace(/^\/+/, "")
    .split("/")
    .map((seg) => encodeURIComponent(seg))
    .join("/");
}

async function fromKv(appId: string): Promise<ZenManifest | null> {
  if (!isKvConfigured()) return null;
  const raw = await kvGet(`zen:manifest:${appId}`);
  if (!raw) return null;
  const manifest = parseManifest(raw);
  // The KV entry must claim the same appId it is keyed under.
  return manifest && manifest.appId === appId ? manifest : null;
}

async function fromGithub(appId: string): Promise<ZenManifest | null> {
  const appCfg = getAppConfig(appId);
  if (!appCfg) return null;
  const manifestPath = encodePathSegments(appCfg.manifestPath);
  const branch = encodeURIComponent(appCfg.branch);

  if (appCfg.private) {
    const token = getGithubToken();
    if (!token) {
      console.warn(`zen-ota: app ${appId} is private but GITHUB_TOKEN is unset`);
      return null;
    }
    const url = `${GITHUB_API_BASE}/repos/${appCfg.repo}/contents/${manifestPath}?ref=${branch}`;
    const resp = await fetchWithTimeout(url, {
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: "application/vnd.github+json",
        "User-Agent": USER_AGENT,
      },
    });
    if (!resp.ok) {
      console.warn(`zen-ota: contents API returned ${resp.status} for ${appId}`);
      return null;
    }
    const data = (await resp.json()) as { content?: unknown; encoding?: unknown };
    if (typeof data.content !== "string" || data.content === "") return null;
    const decoded = Buffer.from(data.content.replace(/\s+/g, ""), "base64").toString("utf8");
    return parseManifest(decoded);
  }

  const rawUrl = `https://raw.githubusercontent.com/${appCfg.repo}/${branch}/${manifestPath}`;
  const resp = await fetchWithTimeout(rawUrl, { headers: { "User-Agent": USER_AGENT } });
  if (!resp.ok) {
    console.warn(`zen-ota: raw manifest returned ${resp.status} for ${appId}`);
    return null;
  }
  return parseManifest(await resp.text());
}

export async function getManifest(appId: string): Promise<ManifestResult> {
  try {
    const kvManifest = await fromKv(appId);
    if (kvManifest) return { source: "kv", manifest: kvManifest };
  } catch (e) {
    console.warn(`zen-ota: KV manifest lookup failed for ${appId}`, e);
  }

  if (!getAppConfig(appId)) {
    return { source: "none", manifest: null, reason: "unknown-app" };
  }

  try {
    const ghManifest = await fromGithub(appId);
    if (ghManifest) return { source: "github", manifest: ghManifest };
  } catch (e) {
    console.warn(`zen-ota: GitHub manifest fetch failed for ${appId}`, e);
  }
  return { source: "none", manifest: null, reason: "unavailable" };
}
