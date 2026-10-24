/**
 * lib/http.ts — small HTTP helpers for the Zen OTA handlers.
 * Every response gets CORS headers; every handler pattern is
 * try/catch → 500 {"error":"internal"}.
 */

import { timingSafeEqual } from "crypto";
import { getAdminToken } from "./env";
import type { VercelRequest, VercelResponse } from "./types";

const CORS_HEADERS: Record<string, string> = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Allow-Methods": "GET,POST,OPTIONS",
  "Access-Control-Allow-Headers": "Authorization,Content-Type",
};

export function applyCors(res: VercelResponse): void {
  for (const [name, value] of Object.entries(CORS_HEADERS)) {
    res.setHeader(name, value);
  }
}

/**
 * Applies CORS and, for OPTIONS requests, answers the preflight directly
 * (204, no body). Returns true when the request was handled.
 */
export function preflight(req: VercelRequest, res: VercelResponse): boolean {
  applyCors(res);
  if ((req.method || "").toUpperCase() === "OPTIONS") {
    res.setHeader("Access-Control-Max-Age", "86400");
    res.status(204);
    res.end();
    return true;
  }
  return false;
}

export function json(
  res: VercelResponse,
  code: number,
  body: unknown,
  extraHeaders?: Record<string, string>,
): void {
  applyCors(res);
  res.setHeader("Content-Type", "application/json; charset=utf-8");
  if (extraHeaders) {
    for (const [name, value] of Object.entries(extraHeaders)) {
      res.setHeader(name, value);
    }
  }
  res.status(code);
  res.json(body);
}

/** JSON error body {"error": msg} with CORS. */
export function err(
  res: VercelResponse,
  code: number,
  msg: string,
  extraHeaders?: Record<string, string>,
): void {
  json(res, code, { error: msg }, extraHeaders);
}

function headerValue(req: VercelRequest, name: string): string | null {
  const value = req.headers ? req.headers[name] : undefined;
  if (value === undefined) return null;
  return Array.isArray(value) ? (value[0] ?? null) : value;
}

/**
 * Query params: prefers Vercel's pre-parsed req.query, falls back to parsing
 * req.url. Array values collapse to their first element.
 */
export function getUrlParams(req: VercelRequest): URLSearchParams {
  const params = new URLSearchParams();
  const url = req.url || "/";
  for (const [key, value] of new URL(url, "http://localhost").searchParams) {
    params.set(key, value);
  }
  const query = req.query;
  if (query && typeof query === "object") {
    for (const [key, value] of Object.entries(query)) {
      if (value === undefined) continue;
      params.set(key, Array.isArray(value) ? String(value[0] ?? "") : String(value));
    }
  }
  return params;
}

async function collectRawBody(req: VercelRequest): Promise<string | null> {
  try {
    const candidate = req as unknown as { [Symbol.asyncIterator]?: unknown };
    if (typeof candidate[Symbol.asyncIterator] !== "function") return null;
    const chunks: Buffer[] = [];
    for await (const chunk of req as unknown as AsyncIterable<unknown>) {
      if (Buffer.isBuffer(chunk)) chunks.push(chunk);
      else if (typeof chunk === "string") chunks.push(Buffer.from(chunk, "utf8"));
      else if (chunk instanceof Uint8Array) chunks.push(Buffer.from(chunk));
      else return null;
    }
    return Buffer.concat(chunks).toString("utf8");
  } catch {
    return null;
  }
}

function parseJsonText(text: string): unknown {
  const trimmed = text.trim();
  if (trimmed === "") return null;
  try {
    return JSON.parse(trimmed);
  } catch {
    return null;
  }
}

/**
 * Read the JSON body of a request. Handles the shapes Vercel may deliver:
 * pre-parsed object, string, Buffer, or (fallback) a streamable IncomingMessage.
 * Returns null when absent or unparsable — callers treat null as 400.
 */
export async function readJsonBody(req: VercelRequest): Promise<unknown> {
  const body = req.body;
  if (body === undefined || body === null) {
    const raw = await collectRawBody(req);
    return raw === null ? null : parseJsonText(raw);
  }
  if (typeof body === "string") return parseJsonText(body);
  if (Buffer.isBuffer(body)) return parseJsonText(body.toString("utf8"));
  return body;
}

/** Derive the public base URL (proto + host) from forwarded headers. */
export function getBaseUrl(req: VercelRequest): string {
  const forwardedHost = headerValue(req, "x-forwarded-host");
  const host =
    forwardedHost && forwardedHost.trim() !== ""
      ? forwardedHost.split(",")[0].trim()
      : (headerValue(req, "host") ?? "localhost");
  const forwardedProto = headerValue(req, "x-forwarded-proto");
  const proto =
    forwardedProto && forwardedProto.trim() !== ""
      ? forwardedProto.split(",")[0].trim()
      : "https";
  return `${proto}://${host}`;
}

/**
 * Parse a versionCode query param (contract §2.1): must be an integer ≥ 0.
 * Returns null when missing/invalid → caller responds 400.
 */
export function parseVersionCodeParam(raw: string | null): number | null {
  if (raw === null) return null;
  const trimmed = raw.trim();
  if (!/^\d{1,15}$/.test(trimmed)) return null;
  const n = Number(trimmed);
  return Number.isSafeInteger(n) && n >= 0 ? n : null;
}

/**
 * Channel query param: defaults to "stable" when absent.
 * Returns null when present but empty/whitespace → caller responds 400.
 */
export function resolveChannelParam(raw: string | null): string | null {
  if (raw === null) return "stable";
  const trimmed = raw.trim();
  return trimmed === "" ? null : trimmed;
}

/**
 * Admin gate (contract §2.4/§2.5):
 *   true        → authorized
 *   "disabled"  → ZEN_ADMIN_TOKEN unset → caller responds 503
 *   false       → missing/bad token → caller responds 401
 * Comparison is timing-safe with a length guard.
 */
export function requireAdmin(req: VercelRequest): boolean | "disabled" {
  const expected = getAdminToken();
  if (!expected) return "disabled";
  const auth = headerValue(req, "authorization");
  if (!auth) return false;
  const match = /^Bearer\s+(.+)$/i.exec(auth.trim());
  const provided = match ? match[1].trim() : "";
  if (provided === "") return false;
  const a = Buffer.from(provided, "utf8");
  const b = Buffer.from(expected, "utf8");
  if (a.length !== b.length) return false; // length guard before timingSafeEqual
  return timingSafeEqual(a, b);
}
