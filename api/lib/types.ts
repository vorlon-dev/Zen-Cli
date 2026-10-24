/**
 * lib/types.ts — shared types for the Zen OTA serverless API.
 *
 * The Zen* interfaces mirror docs/API_CONTRACT.md §1 EXACTLY (snake_case JSON keys
 * as they appear on the wire). The Vercel* interfaces are minimal local stand-ins
 * for @vercel/node types so this project compiles with ZERO npm dependencies;
 * at runtime the real objects passed by Vercel's Node runtime satisfy them.
 */

/* ------------------------------------------------------------------ */
/* Minimal Vercel request/response types (zero-dependency stand-ins)   */
/* ------------------------------------------------------------------ */

export interface VercelRequest {
  method?: string;
  url?: string;
  headers: { [header: string]: string | string[] | undefined };
  query?: { [name: string]: string | string[] | undefined };
  /** Parsed by Vercel when Content-Type is JSON; may be string/Buffer otherwise. */
  body?: unknown;
}

export interface VercelResponse {
  statusCode?: number;
  headersSent?: boolean;
  status(code: number): VercelResponse;
  setHeader(name: string, value: string | number | readonly string[]): VercelResponse;
  json(data: unknown): VercelResponse;
  send(body: unknown): VercelResponse;
  end(chunk?: unknown): VercelResponse;
  write(chunk: unknown, cb?: (err: Error | null | undefined) => void): boolean;
  destroy?(error?: Error): void;
}

/* ------------------------------------------------------------------ */
/* Contract §1 — Release Manifest                                      */
/* ------------------------------------------------------------------ */

export interface ZenAssets {
  /** Always present: https://github.com/OWNER/REPO/releases/download/TAG/ASSET.apk */
  github: string;
  /** Optional R2 mirror URL, null when absent. */
  r2?: string | null;
}

export interface ZenRelease {
  versionName: string;
  versionCode: number;
  /** hex sha256 of the apk file */
  sha256: string;
  size: number;
  /** plain text or markdown */
  releaseNotes?: string;
  /** ISO-8601 timestamp */
  publishedAt: string;
  /** 0–100, 100 = fully rolled out (default) */
  rollout: number;
  /** devices below this versionCode MUST update; 0 = no forced floor */
  minVersionCode: number;
  assets: ZenAssets;
}

export interface ZenChannel {
  /** ordered newest first: history[0] = current, history[1] = previous; cap 25 */
  history: ZenRelease[];
}

export interface ZenManifest {
  schemaVersion: number;
  appId: string;
  updatedAt?: string;
  channels: { [channel: string]: ZenChannel };
}

/** Runtime guard for manifest history entries coming from untrusted JSON. */
export function isZenRelease(value: unknown): value is ZenRelease {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const r = value as Record<string, unknown>;
  return typeof r.versionCode === "number" && Number.isFinite(r.versionCode);
}

/* ------------------------------------------------------------------ */
/* Contract §2.1 — GET /api/check response                             */
/* ------------------------------------------------------------------ */

export interface UpdateCheckRelease {
  versionName: string;
  versionCode: number;
  sha256: string;
  size: number;
  releaseNotes: string | null;
  publishedAt: string;
  rollout: number;
  minVersionCode: number;
  /** ALWAYS points at /api/download (stats + private-repo proxying). */
  downloadUrl: string;
  /** assets.r2 || assets.github; null for private repos. */
  directUrl: string | null;
}

export interface UpdateCheckResponse {
  appId: string;
  channel: string;
  updateAvailable: boolean;
  updateRequired: boolean;
  release: UpdateCheckRelease | null;
  /** ISO-8601 timestamp */
  serverTime: string;
}

/* ------------------------------------------------------------------ */
/* Contract §2.3 — POST /api/event body                                */
/* ------------------------------------------------------------------ */

export type ZenEventType = "install_success" | "install_failed";

export interface ZenEvent {
  appId: string;
  channel: string;
  deviceId?: string;
  versionCode: number;
  type: ZenEventType;
  /** optional free-form string */
  detail?: string;
}

/* ------------------------------------------------------------------ */
/* Contract §2.4 — GET /api/stats response                             */
/* ------------------------------------------------------------------ */

export interface StatsTotals {
  checks: number;
  downloads: number;
  installs: number;
  installFailures: number;
}

export interface StatsSeriesEntry extends StatsTotals {
  date: string; // YYYY-MM-DD (UTC)
}

export interface StatsResponse {
  appId: string;
  days: number;
  series: StatsSeriesEntry[];
  totals: StatsTotals;
}

/* ------------------------------------------------------------------ */
/* lib/manifest.ts result                                              */
/* ------------------------------------------------------------------ */

export type ManifestSource = "kv" | "github" | "none";
