/**
 * api/check.ts — GET /api/check (contract §2.1).
 *
 * Resolution logic:
 *   1. Load manifest (KV if configured, else GitHub raw/Contents API).
 *   2. ch = manifest.channels[channel]; missing → 404 {"error":"unknown channel"}.
 *   3. current = ch.history[0]; previous = ch.history[1] || null.
 *   4. current missing → 200 updateAvailable:false, release:null.
 *   5. Rollout: rollout >= 100 → current. Else bucket the device
 *      (sha256 first-8-hex % 100 < rollout → in group); out-of-group devices get
 *      previous when previous.versionCode > params.versionCode, else no update.
 *      A missing deviceId is treated as "in group".
 *   6. updateAvailable = served.versionCode > versionCode;
 *      updateRequired = versionCode < served.minVersionCode;
 *      release:null when updateAvailable is false (still 200).
 *   7. Record a "checks" stat (best-effort, non-blocking).
 */

import { getAppConfig } from "../lib/env";
import {
  err,
  getBaseUrl,
  getUrlParams,
  json,
  parseVersionCodeParam,
  preflight,
  resolveChannelParam,
} from "../lib/http";
import { EDGE_CACHE_HEADER, getManifest } from "../lib/manifest";
import { isInRolloutGroup } from "../lib/rollout";
import { recordEvent } from "../lib/stats";
import { isZenRelease } from "../lib/types";
import type {
  UpdateCheckRelease,
  UpdateCheckResponse,
  VercelRequest,
  VercelResponse,
  ZenRelease,
} from "../lib/types";

const DEVICE_ID_MAX_LENGTH = 256;

function noUpdateResponse(appId: string, channel: string): UpdateCheckResponse {
  return {
    appId,
    channel,
    updateAvailable: false,
    updateRequired: false,
    release: null,
    serverTime: new Date().toISOString(),
  };
}

/** assets.r2 || assets.github (§2.1 directUrl for public repos). */
function pickDirectUrl(release: ZenRelease): string | null {
  const assets = release && typeof release.assets === "object" ? release.assets : null;
  if (!assets) return null;
  if (typeof assets.r2 === "string" && assets.r2 !== "") return assets.r2;
  if (typeof assets.github === "string" && assets.github !== "") return assets.github;
  return null;
}

function toCheckRelease(
  release: ZenRelease,
  appId: string,
  channel: string,
  baseUrl: string,
): UpdateCheckRelease {
  const appCfg = getAppConfig(appId);
  // Contract: private repos MUST NOT expose directUrl. Unknown app configs
  // (manifest served from KV only) are treated conservatively as private.
  const isPrivate = appCfg ? appCfg.private === true : true;
  const directUrl = isPrivate ? null : pickDirectUrl(release);
  const downloadUrl =
    `${baseUrl}/api/download?appId=${encodeURIComponent(appId)}` +
    `&channel=${encodeURIComponent(channel)}` +
    `&versionCode=${release.versionCode}`;
  return {
    versionName: typeof release.versionName === "string" ? release.versionName : "",
    versionCode: release.versionCode,
    sha256: typeof release.sha256 === "string" ? release.sha256 : "",
    size: typeof release.size === "number" ? release.size : 0,
    releaseNotes: typeof release.releaseNotes === "string" ? release.releaseNotes : null,
    publishedAt: typeof release.publishedAt === "string" ? release.publishedAt : "",
    rollout: typeof release.rollout === "number" ? release.rollout : 100,
    minVersionCode: typeof release.minVersionCode === "number" ? release.minVersionCode : 0,
    downloadUrl,
    directUrl,
  };
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

    const deviceId = (params.get("deviceId") || "").trim();
    if (deviceId.length > DEVICE_ID_MAX_LENGTH) {
      err(res, 400, "deviceId too long");
      return;
    }

    // Step 1–2: manifest + channel. Unknown appId → 404; upstream failure → 500.
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
    if (!ch) {
      err(res, 404, "unknown channel");
      return;
    }

    // Step 3–4: current/previous. A channel without history → 200, no update.
    const history: unknown[] = Array.isArray(ch.history) ? ch.history : [];
    const current = history.length > 0 ? history[0] : null;
    const previous = history.length > 1 ? history[1] : null;
    if (!isZenRelease(current)) {
      json(res, 200, noUpdateResponse(appId, channel), EDGE_CACHE_HEADER);
      return;
    }

    // Step 5: staged rollout. Missing deviceId ⇒ treated as "in group".
    const rollout = typeof current.rollout === "number" ? current.rollout : 100;
    let served: ZenRelease | null = current;
    if (rollout < 100 && deviceId !== "") {
      const inGroup = isInRolloutGroup(appId, channel, deviceId, current.versionCode, rollout);
      if (!inGroup) {
        served =
          isZenRelease(previous) && previous.versionCode > versionCode ? previous : null;
      }
    }

    // Step 6: serve decision.
    const updateAvailable = served !== null && served.versionCode > versionCode;
    const minVersionCode =
      served && typeof served.minVersionCode === "number" ? served.minVersionCode : 0;
    const updateRequired = served !== null && versionCode < minVersionCode;
    const response: UpdateCheckResponse = {
      appId,
      channel,
      updateAvailable,
      updateRequired,
      release:
        updateAvailable && served
          ? toCheckRelease(served, appId, channel, getBaseUrl(req))
          : null,
      serverTime: new Date().toISOString(),
    };

    // Step 7: best-effort stat, never blocks or fails the request.
    void recordEvent(appId, "checks").catch(() => undefined);

    json(res, 200, response, EDGE_CACHE_HEADER);
  } catch (e) {
    console.error("zen-ota: /api/check internal error", e);
    if (!res.headersSent) err(res, 500, "internal");
    else res.end();
  }
}
