/**
 * lib/stats.ts — best-effort usage counters in Upstash KV (optional).
 *
 * Keys (INCR approach per contract §4):
 *   zen:stats:{appId}:{YYYY-MM-DD}:{field}   daily counters, EXPIRE 90 days
 *   zen:stats:total:{appId}:{field}          all-time counters
 * where field ∈ checks | downloads | installs | installFailures.
 *
 * Aggregation for /api/stats uses chunked MGETs over the UTC date range.
 * NOTHING in this module throws — stats failures are swallowed with a warn so
 * they can never break a check/download/event request.
 */

import { isKvConfigured, kvPipeline } from "./kv";
import type { StatsResponse, StatsSeriesEntry, StatsTotals } from "./types";

export const STAT_FIELDS = ["checks", "downloads", "installs", "installFailures"] as const;
export type StatField = (typeof STAT_FIELDS)[number];

/** 90 days in seconds (Upstash EXPIRE unit). */
const DAY_TTL_SECONDS = 90 * 24 * 60 * 60;
const DAY_MS = 24 * 60 * 60 * 1000;
/** Max days retrievable (contract §2.4). */
export const MAX_STATS_DAYS = 90;
/** Keys per MGET command inside a pipeline chunk (keeps bodies small). */
const MGET_CHUNK_KEYS = 60;

export function utcDateKey(date: Date): string {
  return date.toISOString().slice(0, 10);
}

function dayKey(appId: string, date: string, field: StatField): string {
  return `zen:stats:${appId}:${date}:${field}`;
}

function totalKey(appId: string, field: StatField): string {
  return `zen:stats:total:${appId}:${field}`;
}

/**
 * Record an event (best-effort, never throws). `n` defaults to 1.
 * One pipeline round-trip: INCRBY day key (+EXPIRE 90d) and INCRBY total key.
 */
export async function recordEvent(appId: string, field: StatField, n = 1): Promise<void> {
  try {
    if (!isKvConfigured()) return;
    if (!Number.isFinite(n) || n <= 0) return;
    const date = utcDateKey(new Date());
    const replies = await kvPipeline([
      ["INCRBY", dayKey(appId, date, field), String(n)],
      ["EXPIRE", dayKey(appId, date, field), String(DAY_TTL_SECONDS)],
      ["INCRBY", totalKey(appId, field), String(n)],
    ]);
    if (!replies) {
      // KV unconfigured or unreachable — stats are optional, nothing to do.
      return;
    }
  } catch (e) {
    console.warn(`zen-ota: recordEvent(${appId},${field}) failed`, e);
  }
}

/**
 * Chunked MGET via pipeline (avoids huge request URLs for 90-day ranges).
 * Returns aligned values array, or null when KV is unconfigured/unreachable.
 */
async function mgetChunked(keys: string[]): Promise<(string | null)[] | null> {
  const commands: string[][] = [];
  for (let i = 0; i < keys.length; i += MGET_CHUNK_KEYS) {
    commands.push(["MGET", ...keys.slice(i, i + MGET_CHUNK_KEYS)]);
  }
  const replies = await kvPipeline(commands);
  if (!replies) return null;
  const out: (string | null)[] = [];
  for (const reply of replies) {
    const value = reply && typeof reply === "object" ? reply.result : null;
    if (Array.isArray(value)) {
      for (const v of value) out.push(typeof v === "string" ? v : null);
    } else {
      // Malformed chunk — pad so alignment with `keys` is preserved.
      const expected = Math.min(MGET_CHUNK_KEYS, keys.length - out.length);
      for (let i = 0; i < expected; i++) out.push(null);
    }
  }
  return out.length === keys.length ? out : null;
}

function toCounter(value: string | null | undefined): number {
  const n = value !== null && value !== undefined ? Number(value) : 0;
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0;
}

/** Clamp a raw `days` query param: default 7, range 1..90 (integers). */
export function clampDays(raw: string | null | undefined): number {
  const n = raw !== null && raw !== undefined && raw !== "" ? Number(raw) : NaN;
  if (!Number.isFinite(n)) return 7;
  return Math.min(MAX_STATS_DAYS, Math.max(1, Math.floor(n)));
}

function zeroTotals(): StatsTotals {
  return { checks: 0, downloads: 0, installs: 0, installFailures: 0 };
}

/**
 * Build the §2.4 stats payload. Returns null ONLY when KV is not configured
 * (→ /api/stats answers 501). Transient KV errors resolve to zero-filled data
 * (warned) so an outage never 500s the admin endpoint.
 */
export async function getStats(appId: string, days: number): Promise<StatsResponse | null> {
  try {
    if (!isKvConfigured()) return null;
    const clamped = Math.min(MAX_STATS_DAYS, Math.max(1, Math.floor(days)));

    // Date range oldest -> newest, inclusive of today (UTC).
    const dates: string[] = [];
    const nowMs = Date.now();
    for (let i = 0; i < clamped; i++) {
      dates.push(utcDateKey(new Date(nowMs - i * DAY_MS)));
    }
    dates.reverse();

    const keys: string[] = [];
    const dateByKey = new Map<string, string>();
    for (const date of dates) {
      for (const field of STAT_FIELDS) {
        const key = dayKey(appId, date, field);
        keys.push(key);
        dateByKey.set(key, date);
      }
    }

    const series: StatsSeriesEntry[] = dates.map((date) => ({ date, ...zeroTotals() }));
    const indexByDate = new Map<string, number>();
    dates.forEach((date, i) => indexByDate.set(date, i));

    const values = (await mgetChunked(keys)) ?? keys.map(() => null);
    for (let i = 0; i < keys.length; i++) {
      const date = dateByKey.get(keys[i]);
      const idx = date !== undefined ? indexByDate.get(date) : undefined;
      if (idx === undefined) continue;
      const field = STAT_FIELDS[i % STAT_FIELDS.length];
      series[idx][field] = toCounter(values[i]);
    }

    const totalsKeys = STAT_FIELDS.map((field) => totalKey(appId, field));
    const totalsValues = (await mgetChunked(totalsKeys)) ?? totalsKeys.map(() => null);
    const totals = zeroTotals();
    STAT_FIELDS.forEach((field, i) => {
      totals[field] = toCounter(totalsValues[i]);
    });

    return { appId, days: clamped, series, totals };
  } catch (e) {
    console.warn(`zen-ota: getStats(${appId}) failed`, e);
    return null;
  }
}
