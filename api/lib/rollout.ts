/**
 * lib/rollout.ts — staged-rollout bucketing per API_CONTRACT.md §2.1 step 5.
 *
 *   bucket = sha256_hex(`${appId}:${channel}:${deviceId}:${versionCode}`)
 *   take the FIRST 8 HEX CHARS as an unsigned integer, then `% 100`
 *   device is in the rollout group iff bucket < rollout
 */

import { createHash } from "crypto";

/**
 * Deterministic 0–99 bucket for a device.
 *
 * Self-test (pure function, verified with `crypto` on Node 24):
 *   bucketFor("com.example.app", "stable", "device-abc", 24)
 *     sha256 = 6fd8bc16 9f1c...  -> first 8 hex = "6fd8bc16" -> % 100 === 2
 *   bucketFor("com.example.app", "beta", "11111111-2222-3333-4444-555555555555", 7)
 *     sha256 = 960c022c ...      -> first 8 hex = "960c022c" -> % 100 === 88
 */
export function bucketFor(
  appId: string,
  channel: string,
  deviceId: string,
  versionCode: number,
): number {
  const hex = createHash("sha256")
    .update(`${appId}:${channel}:${deviceId}:${versionCode}`)
    .digest("hex");
  const first8 = parseInt(hex.slice(0, 8), 16);
  return first8 % 100;
}

/**
 * True when the device belongs to the rollout group for `rolloutPercent` (0–100).
 * 100+ = always true, <= 0 = always false. Non-finite rollout is treated as
 * fully rolled out (defensive against malformed manifests).
 */
export function isInRolloutGroup(
  appId: string,
  channel: string,
  deviceId: string,
  versionCode: number,
  rolloutPercent: number,
): boolean {
  if (!Number.isFinite(rolloutPercent) || rolloutPercent >= 100) return true;
  if (rolloutPercent <= 0) return false;
  return bucketFor(appId, channel, deviceId, versionCode) < rolloutPercent;
}
