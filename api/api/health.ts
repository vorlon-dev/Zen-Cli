/**
 * api/health.ts — GET /api/health (liveness probe).
 * 200 {"ok":true,"service":"zen-ota","time":…} — no auth, no caching.
 */

import { err, json, preflight } from "../lib/http";
import type { VercelRequest, VercelResponse } from "../lib/types";

export default async function handler(req: VercelRequest, res: VercelResponse): Promise<void> {
  try {
    if (preflight(req, res)) return;
    const method = (req.method || "GET").toUpperCase();
    if (method !== "GET") {
      err(res, 405, "method not allowed");
      return;
    }
    json(
      res,
      200,
      { ok: true, service: "zen-ota", time: new Date().toISOString() },
      { "Cache-Control": "no-store" },
    );
  } catch (e) {
    console.error("zen-ota: /api/health internal error", e);
    if (!res.headersSent) err(res, 500, "internal");
    else res.end();
  }
}
