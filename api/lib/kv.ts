/**
 * lib/kv.ts — minimal Upstash REST client over global fetch (ZERO npm deps).
 *
 * Primary style is the simple path form:
 *   POST {url}/get/{key}
 *   POST {url}/set/{key}/{urlencoded value}[/{EX}/{ttl}]
 *   POST {url}/incr/{key}
 * with `Authorization: Bearer {KV_REST_API_TOKEN}`.
 *
 * Values that would produce very long URLs (e.g. a whole manifest) are sent via
 * the documented pipeline endpoint instead: POST {url}/pipeline with a JSON
 * array-of-commands body — functionally identical, no URL length limit.
 *
 * Every function degrades gracefully: when KV_REST_API_URL / KV_REST_API_TOKEN
 * are unset (or a call fails) it returns null/false instead of throwing.
 */

import { getKvRestConfig } from "./env";

const KV_TIMEOUT_MS = 10_000;
/** URL-encoded values longer than this go through the pipeline body endpoint. */
const MAX_PATH_VALUE_LENGTH = 4_000;
let cachedCfg: { url: string; token: string } | null | undefined;

function cfg(): { url: string; token: string } | null {
  if (cachedCfg === undefined) cachedCfg = getKvRestConfig();
  return cachedCfg;
}

/** True when KV_REST_API_URL + KV_REST_API_TOKEN are both set. */
export function isKvConfigured(): boolean {
  return cfg() !== null;
}

function seg(part: string): string {
  return encodeURIComponent(part);
}

type KvRawResult = string | number | (string | null)[] | null;

/** POST {url}/{command}/{args...} (path style). Returns Upstash `result` or null. */
async function runPath(command: string, args: string[]): Promise<KvRawResult> {
  const c = cfg();
  if (!c) return null;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), KV_TIMEOUT_MS);
  try {
    const path = [command, ...args].map(seg).join("/");
    const resp = await fetch(`${c.url}/${path}`, {
      method: "POST",
      headers: { Authorization: `Bearer ${c.token}` },
      signal: controller.signal,
    });
    if (!resp.ok) {
      console.warn(`zen-ota: KV ${command} failed with HTTP ${resp.status}`);
      return null;
    }
    const data = (await resp.json()) as { result?: KvRawResult; error?: unknown };
    if (data && typeof data === "object" && data.error) {
      console.warn("zen-ota: KV error response", data.error);
      return null;
    }
    return data && typeof data === "object" && "result" in data ? (data.result ?? null) : null;
  } catch (e) {
    console.warn(`zen-ota: KV ${command} request failed`, e);
    return null;
  } finally {
    clearTimeout(timer);
  }
}

export interface KvPipelineReply {
  result: unknown;
  error?: unknown;
}

/**
 * POST {url}/pipeline with body = JSON array of command arg arrays.
 * Returns the per-command replies, or null when unconfigured/failed.
 */
export async function kvPipeline(commands: string[][]): Promise<KvPipelineReply[] | null> {
  const c = cfg();
  if (!c) return null;
  if (commands.length === 0) return [];
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), KV_TIMEOUT_MS);
  try {
    const resp = await fetch(`${c.url}/pipeline`, {
      method: "POST",
      headers: {
        Authorization: `Bearer ${c.token}`,
        "Content-Type": "application/json",
      },
      body: JSON.stringify(commands),
      signal: controller.signal,
    });
    if (!resp.ok) {
      console.warn(`zen-ota: KV pipeline failed with HTTP ${resp.status}`);
      return null;
    }
    const data: unknown = await resp.json();
    if (!Array.isArray(data)) {
      console.warn("zen-ota: KV pipeline returned non-array body");
      return null;
    }
    return data as KvPipelineReply[];
  } catch (e) {
    console.warn("zen-ota: KV pipeline request failed", e);
    return null;
  } finally {
    clearTimeout(timer);
  }
}

/** GET a string value; null when missing / unconfigured / failed. */
export async function kvGet(key: string): Promise<string | null> {
  const result = await runPath("get", [key]);
  return typeof result === "string" ? result : null;
}

/**
 * SET a value (URL-encoded in the path). Optional TTL in seconds (EX).
 * Returns true on "OK", false when unconfigured/failed.
 */
export async function kvSet(key: string, value: string, ttlSeconds?: number): Promise<boolean> {
  const encoded = encodeURIComponent(value);
  if (encoded.length <= MAX_PATH_VALUE_LENGTH) {
    const args = [key, value];
    if (ttlSeconds !== undefined) args.push("EX", String(ttlSeconds));
    const result = await runPath("set", args);
    return result === "OK";
  }
  // Large values (e.g. manifests) must not travel in the URL — use the
  // pipeline endpoint with a JSON body instead.
  const cmd: string[] = ["SET", key, value];
  if (ttlSeconds !== undefined) cmd.push("EX", String(ttlSeconds));
  const replies = await kvPipeline([cmd]);
  if (!replies || replies.length !== 1) return false;
  return replies[0] !== null && replies[0].result === "OK";
}

/** INCR a counter; returns the new value, or null when unconfigured/failed. */
export async function kvIncr(key: string): Promise<number | null> {
  const result = await runPath("incr", [key]);
  return typeof result === "number" ? result : null;
}

/**
 * MGET several keys (path style). Returns an aligned array of string|null,
 * or null when unconfigured/failed. Prefer lib/stats' chunked pipeline MGET
 * for large key sets (path length limits).
 */
export async function kvMget(keys: string[]): Promise<(string | null)[] | null> {
  if (keys.length === 0) return [];
  const result = await runPath("mget", keys);
  return Array.isArray(result) ? (result as (string | null)[]) : null;
}
