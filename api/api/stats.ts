/**
 * api/stats.ts — GET /api/stats (contract §2.4, admin).
 *
 * Auth: Authorization: Bearer ZEN_ADMIN_TOKEN
 *   unset token → 503 {"error":"admin disabled"}; bad/missing → 401.
 * KV not configured → 501 {"error":"stats require KV"}.
 * Query: appId (required), days (default 7, clamped to 1..90).
 */

import { err, getUrlParams, json, preflight, requireAdmin } from "../lib/http";
import { isKvConfigured } from "../lib/kv";
import { clampDays, getStats } from "../lib/stats";
import type { VercelRequest, VercelResponse } from "../lib/types";

export default async function handler(req: VercelRequest, res: VercelResponse): Promise<void> {
  try {
    if (preflight(req, res)) return;
    const method = (req.method || "GET").toUpperCase();
    if (method !== "GET") {
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

    if (!isKvConfigured()) {
      err(res, 501, "stats require KV");
      return;
    }

    const params = getUrlParams(req);
    const appId = (params.get("appId") || "").trim();
    if (appId === "") {
      err(res, 400, "appId is required");
      return;
    }

    const days = clampDays(params.get("days"));
    const data = await getStats(appId, days);
    if (!data) {
      // KV disappeared between the check and the read — treat as unconfigured.
      err(res, 501, "stats require KV");
      return;
    }

    json(res, 200, data);
  } catch (e) {
    console.error("zen-ota: /api/stats internal error", e);
    if (!res.headersSent) err(res, 500, "internal");
    else res.end();
  }
}
