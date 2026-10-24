/**
 * api/download.ts — GET /api/download (contract §2.2).
 *
 * Query params: appId (required), channel (default stable), versionCode (required),
 * deviceId (optional, accepted for symmetry, not needed for resolution).
 *
 * Resolution: manifest → channel history → entry with matching versionCode.
 * Not found → 404 {"error":"release not found"}.
 *
 * - Public repo → 302 redirect to assets.r2 || assets.github.
 * - Private repo (ZEN_APPS[].private) → stream the GitHub release asset through
 *   the function: resolve the tag + asset name from assets.github
 *   (github.com/OWNER/REPO/releases/download/TAG/ASSET.apk), look the asset id up
 *   via GET /repos/OWNER/REPO/releases/tags/TAG, then GET
 *   /repos/OWNER/REPO/releases/assets/{id} with Accept: application/octet-stream,
 *   piping the body with Content-Length and
 *   Content-Type: application/vnd.android.package-archive. Range headers are
 *   passed through (resume support).
 *
 * A "downloads" stat is recorded best-effort for both paths.
 */

import { Readable } from "stream";
import type { ReadableStream as WebReadableStream } from "stream/web";
import { getGithubToken, getAppConfig } from "../lib/env";
import {
  applyCors,
  err,
  getUrlParams,
  parseVersionCodeParam,
  preflight,
  resolveChannelParam,
} from "../lib/http";
import { getManifest } from "../lib/manifest";
import { recordEvent } from "../lib/stats";
import { isZenRelease } from "../lib/types";
import type { VercelRequest, VercelResponse, ZenRelease } from "../lib/types";

const FETCH_TIMEOUT_MS = 30_000;
const GITHUB_API_BASE = "https://api.github.com";
const USER_AGENT = "zen-ota-api";
const APK_CONTENT_TYPE = "application/vnd.android.package-archive";

function fetchWithTimeout(url: string, init?: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), FETCH_TIMEOUT_MS);
  return fetch(url, { ...init, signal: controller.signal }).finally(() => clearTimeout(timer));
}

function headerValue(req: VercelRequest, name: string): string | null {
  const value = req.headers ? req.headers[name] : undefined;
  if (value === undefined) return null;
  return Array.isArray(value) ? (value[0] ?? null) : value;
}

/** assets.r2 || assets.github, null when the manifest entry lacks both. */
function pickAssetUrl(release: ZenRelease): string | null {
  const assets = release && typeof release.assets === "object" ? release.assets : null;
  if (!assets) return null;
  if (typeof assets.r2 === "string" && assets.r2 !== "") return assets.r2;
  if (typeof assets.github === "string" && assets.github !== "") return assets.github;
  return null;
}

/**
 * Split a GitHub release download URL into its API coordinates:
 *   https://github.com/OWNER/REPO/releases/download/TAG/ASSET.apk
 *   → { owner, repo, tag, assetName }
 */
function parseGithubReleaseAssetUrl(
  url: string,
): { owner: string; repo: string; tag: string; assetName: string } | null {
  try {
    const parsed = new URL(url);
    if (parsed.hostname !== "github.com" && parsed.hostname !== "www.github.com") return null;
    const parts = parsed.pathname.split("/").filter((p) => p !== "");
    if (parts.length < 6) return null;
    const [owner, repo, releases, download, tag, ...assetParts] = parts;
    if (releases !== "releases" || download !== "download" || assetParts.length === 0) return null;
    if (owner === "" || repo === "" || tag === "") return null;
    return {
      owner: decodeURIComponent(owner),
      repo: decodeURIComponent(repo),
      tag: decodeURIComponent(tag),
      assetName: assetParts.map(decodeURIComponent).join("/"),
    };
  } catch {
    return null;
  }
}

function githubHeaders(token: string, accept: string): Record<string, string> {
  return {
    Authorization: `Bearer ${token}`,
    Accept: accept,
    "User-Agent": USER_AGENT,
  };
}

/**
 * Stream a private-release asset through this function.
 * The caller has already verified the release entry exists in the manifest.
 */
async function streamPrivateAsset(
  req: VercelRequest,
  res: VercelResponse,
  release: ZenRelease,
): Promise<void> {
  const githubUrl = release.assets && typeof release.assets.github === "string"
    ? release.assets.github
    : null;
  if (!githubUrl) {
    err(res, 500, "invalid manifest: missing assets.github");
    return;
  }
  const coords = parseGithubReleaseAssetUrl(githubUrl);
  if (!coords) {
    err(res, 500, "invalid manifest: unparseable assets.github");
    return;
  }
  const token = getGithubToken();
  if (token === "") {
    err(res, 500, "server not configured for private downloads");
    return;
  }

  // 1) Resolve the GitHub release by tag.
  const releaseUrl =
    `${GITHUB_API_BASE}/repos/${encodeURIComponent(coords.owner)}/${encodeURIComponent(coords.repo)}` +
    `/releases/tags/${encodeURIComponent(coords.tag)}`;
  const releaseResp = await fetchWithTimeout(releaseUrl, {
    headers: githubHeaders(token, "application/vnd.github+json"),
  });
  if (releaseResp.status === 404) {
    err(res, 404, "release not found");
    return;
  }
  if (!releaseResp.ok) {
    console.error(`zen-ota: release lookup failed (${releaseResp.status}) for ${coords.repo}`);
    err(res, 502, "upstream error");
    return;
  }
  const releaseJson = (await releaseResp.json()) as { assets?: unknown };
  const assets: Array<{ id?: unknown; name?: unknown }> = Array.isArray(releaseJson.assets)
    ? (releaseJson.assets as Array<{ id?: unknown; name?: unknown }>)
    : [];
  const asset = assets.find((a) => a && a.name === coords.assetName);
  if (!asset || typeof asset.id !== "number") {
    err(res, 404, "asset not found");
    return;
  }

  // 2) Fetch the asset as an octet-stream (follows the redirect to the CDN).
  const upstreamHeaders = githubHeaders(token, "application/octet-stream");
  const range = headerValue(req, "range");
  if (range) upstreamHeaders.Range = range; // pass-through for resume support
  const assetUrl =
    `${GITHUB_API_BASE}/repos/${encodeURIComponent(coords.owner)}/${encodeURIComponent(coords.repo)}` +
    `/releases/assets/${asset.id}`;
  const upstream = await fetchWithTimeout(assetUrl, {
    headers: upstreamHeaders,
    redirect: "follow",
  });
  if (!upstream.ok || !upstream.body) {
    console.error(`zen-ota: asset fetch failed (${upstream.status}) for ${coords.assetName}`);
    err(res, upstream.status === 404 ? 404 : 502, "upstream error");
    return;
  }

  res.status(upstream.status); // 200, or 206 when a Range was honored
  res.setHeader("Content-Type", APK_CONTENT_TYPE);
  const contentLength = upstream.headers.get("content-length");
  if (contentLength) res.setHeader("Content-Length", contentLength);
  const contentRange = upstream.headers.get("content-range");
  if (contentRange) res.setHeader("Content-Range", contentRange);
  const acceptRanges = upstream.headers.get("accept-ranges");
  if (acceptRanges) res.setHeader("Accept-Ranges", acceptRanges);
  applyCors(res);

  // 3) Pipe the web stream through with backpressure; destroy on client close.
  const nodeStream = Readable.fromWeb(upstream.body as unknown as WebReadableStream);
  nodeStream.on("error", (e: Error) => {
    console.error("zen-ota: download stream error", e);
    res.destroy ? res.destroy(e) : res.end();
  });
  const resAny = res as unknown as { on?: (event: string, listener: () => void) => void };
  if (typeof resAny.on === "function") {
    resAny.on("close", () => nodeStream.destroy());
  }
  nodeStream.pipe(res as unknown as NodeJS.WritableStream);
}

export default async function handler(req: VercelRequest, res: VercelResponse): Promise<void> {
  try {
    if (preflight(req, res)) return;
    const method = (req.method || "GET").toUpperCase();
    if (method !== "GET") {
      err(res, 405, "method not allowed");
      return;
    }

    const params = getUrlParams(req);

    const appId = (params.get("appId") || "").trim();
    if (appId === "") {
      err(res, 400, "appId is required");
      return;
    }

    const versionCode = parseVersionCodeParam(params.get("versionCode"));
    if (versionCode === null) {
      err(res, 400, "versionCode must be a non-negative integer");
      return;
    }

    const channel = resolveChannelParam(params.get("channel"));
    if (channel === null) {
      err(res, 400, "channel must be a non-empty string");
      return;
    }

    const result = await getManifest(appId);
    if (!result.manifest) {
      if (result.reason === "unknown-app") err(res, 404, "unknown appId");
      else err(res, 500, "upstream error");
      return;
    }
    const channels = result.manifest.channels || {};
    const ch = Object.prototype.hasOwnProperty.call(channels, channel)
      ? channels[channel]
      : undefined;
    const history: unknown[] = ch && Array.isArray(ch.history) ? ch.history : [];
    const entry = history.find(
      (r): r is ZenRelease => isZenRelease(r) && r.versionCode === versionCode,
    );
    if (!entry) {
      err(res, 404, "release not found");
      return;
    }

    const appCfg = getAppConfig(appId);
    const isPrivate = appCfg ? appCfg.private === true : false;

    // Best-effort stat for both paths; never blocks or fails the download.
    void recordEvent(appId, "downloads").catch(() => undefined);

    if (isPrivate) {
      await streamPrivateAsset(req, res, entry);
      return;
    }

    const target = pickAssetUrl(entry);
    if (!target) {
      err(res, 500, "invalid manifest: missing asset url");
      return;
    }
    res.status(302);
    res.setHeader("Location", target);
    res.setHeader("Content-Length", "0");
    res.end();
  } catch (e) {
    console.error("zen-ota: /api/download internal error", e);
    if (!res.headersSent) err(res, 500, "internal");
    else res.end();
  }
}
