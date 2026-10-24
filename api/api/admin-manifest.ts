/**
 * api/admin-manifest.ts — POST + GET /api/admin/manifest (contract §2.5, §2.6).
 *
 * Vercel maps this file to /api/admin-manifest; a rewrite in vercel.json also
 * exposes the contract path /api/admin/manifest on it.
 *
 * Auth (both methods): Authorization: Bearer ZEN_ADMIN_TOKEN
 *   unset → 503 {"error":"admin disabled"}; bad/missing → 401.
 *
 * POST: validates the manifest shape (schemaVersion===1, appId string,
 *   channels object, each channel.history a non-empty array of entries with
 *   valid integer versionCode) and writes it to KV under zen:manifest:<appId>
 *   for instant propagation. 200 {"ok":true}. KV unconfigured → 501.
 *
 * GET: ?appId=… → resolved manifest (KV first, else repo source),
 *   200 {"source":"kv"|"github","manifest":{…}} or 404.
 */

import { err, getUrlParams, json, preflight, readJsonBody, requireAdmin } from "../lib/http";
import { isKvConfigured, kvSet } from "../lib/kv";
import { getManifest } from "../lib/manifest";
import type { VercelRequest, VercelResponse, ZenManifest } from "../lib/types";

/** Contract §1: history is capped at 25 entries. */
const MAX_HISTORY = 25;

function validateManifestShape(value: unknown): string | null {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return "manifest must be a JSON object";
  }
  const m = value as Record<string, unknown>;
  if (m.schemaVersion !== 1) return "schemaVersion must be 1";
  if (typeof m.appId !== "string" || m.appId.trim() === "") {
    return "appId must be a non-empty string";
  }
  if (!m.channels || typeof m.channels !== "object" || Array.isArray(m.channels)) {
    return "channels must be an object";
  }
  for (const [name, channel] of Object.entries(m.channels as Record<string, unknown>)) {
    if (!channel || typeof channel !== "object" || Array.isArray(channel)) {
      return `channel ${name} must be an object`;
    }
    const history = (channel as Record<string, unknown>).history;
    if (!Array.isArray(history) || history.length === 0) {
      return `channel ${name} history must be a non-empty array`;
    }
    if (history.length > MAX_HISTORY) {
      return `channel ${name} history exceeds ${MAX_HISTORY} entries`;
    }
    for (let i = 0; i < history.length; i++) {
      const release = history[i];
      if (!release || typeof release !== "object" || Array.isArray(release)) {
        return `channel ${name} history[${i}] must be an object`;
      }
      const versionCode = (release as Record<string, unknown>).versionCode;
      if (
        typeof versionCode !== "number" ||
        !Number.isInteger(versionCode) ||
        versionCode < 0
      ) {
        return `channel ${name} history[${i}] versionCode must be a non-negative integer`;
      }
    }
  }
  return null;
}

async function handlePost(req: VercelRequest, res: VercelResponse): Promise<void> {
  if (!isKvConfigured()) {
    err(res, 501, "KV not configured");
    return;
  }
  const body = await readJsonBody(req);
  const invalid = validateManifestShape(body);
  if (invalid !== null) {
    err(res, 400, invalid);
    return;
  }
  const manifest = body as ZenManifest;
  const ok = await kvSet(
    `zen:manifest:${manifest.appId.trim()}`,
    JSON.stringify(manifest),
  );
  if (!ok) {
    err(res, 500, "failed to write manifest");
    return;
  }
  json(res, 200, { ok: true });
}

async function handleGet(req: VercelRequest, res: VercelResponse): Promise<void> {
  const params = getUrlParams(req);
  const appId = (params.get("appId") || "").trim();
  if (appId === "") {
    err(res, 400, "appId is required");
    return;
  }
  const result = await getManifest(appId);
  if (!result.manifest || result.source === "none") {
    err(res, 404, "manifest not found");
    return;
  }
  json(res, 200, { source: result.source, manifest: result.manifest });
}

export default async function handler(req: VercelRequest, res: VercelResponse): Promise<void> {
  try {
    if (preflight(req, res)) return;
    const method = (req.method || "GET").toUpperCase();
    if (method !== "GET" && method !== "POST") {
      err(res, 405, "method not allowed");
      return;
    }

    const gate = requireAdmin(req);
    if (gate === "disabled") {
      err(res, 503, "admin disabled");
      return;
    }
    if (gate === false) {
      err(res, 401, "unauthorized");
      return;
    }

    if (method === "POST") await handlePost(req, res);
    else await handleGet(req, res);
  } catch (e) {
    console.error("zen-ota: /api/admin/manifest internal error", e);
    if (!res.headersSent) err(res, 500, "internal");
    else res.end();
  }
}
