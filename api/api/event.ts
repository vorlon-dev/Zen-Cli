/**
 * api/event.ts — POST /api/event (contract §2.3).
 *
 * Body JSON: { appId, channel, deviceId, versionCode, type, detail }
 *   type ∈ "install_success" | "install_failed"
 * Records an "installs" / "installFailures" stat (best-effort KV).
 * 204 No Content on success; 400 on invalid body.
 */

import { applyCors, err, preflight, readJsonBody } from "../lib/http";
import { recordEvent } from "../lib/stats";
import type { StatField } from "../lib/stats";
import type { VercelRequest, VercelResponse } from "../lib/types";

const VALID_TYPES = ["install_success", "install_failed"] as const;
type RawEventType = (typeof VALID_TYPES)[number];

function statFieldFor(type: RawEventType): StatField {
  return type === "install_success" ? "installs" : "installFailures";
}

export default async function handler(req: VercelRequest, res: VercelResponse): Promise<void> {
  try {
    if (preflight(req, res)) return;
    const method = (req.method || "POST").toUpperCase();
    if (method !== "POST") {
      err(res, 405, "method not allowed");
      return;
    }

    const body = await readJsonBody(req);
    if (!body || typeof body !== "object" || Array.isArray(body)) {
      err(res, 400, "invalid JSON body");
      return;
    }
    const b = body as Record<string, unknown>;

    const appId = b.appId;
    if (typeof appId !== "string" || appId.trim() === "") {
      err(res, 400, "appId must be a non-empty string");
      return;
    }

    const type = b.type;
    if (typeof type !== "string" || !(VALID_TYPES as readonly string[]).includes(type)) {
      err(res, 400, `type must be one of ${VALID_TYPES.join(" | ")}`);
      return;
    }

    const versionCode = b.versionCode;
    if (
      typeof versionCode !== "number" ||
      !Number.isInteger(versionCode) ||
      versionCode < 0
    ) {
      err(res, 400, "versionCode must be a non-negative integer");
      return;
    }

    // channel is validated as a string per contract (it is not part of the
    // stats key, so after validation it is intentionally not used further).
    if (b.channel !== undefined && b.channel !== null) {
      if (typeof b.channel !== "string" || b.channel.trim() === "") {
        err(res, 400, "channel must be a non-empty string");
        return;
      }
    }

    if (b.deviceId !== undefined && b.deviceId !== null && typeof b.deviceId !== "string") {
      err(res, 400, "deviceId must be a string");
      return;
    }

    if (b.detail !== undefined && b.detail !== null && typeof b.detail !== "string") {
      err(res, 400, "detail must be a string");
      return;
    }

    await recordEvent(appId.trim(), statFieldFor(type as RawEventType));

    // 204 No Content — never a body.
    applyCors(res);
    res.status(204);
    res.end();
  } catch (e) {
    console.error("zen-ota: /api/event internal error", e);
    if (!res.headersSent) err(res, 500, "internal");
    else res.end();
  }
}
