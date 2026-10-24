#!/usr/bin/env node
/**
 * Zen CLI — npm dispatcher.
 *
 * Resolution order (esbuild-style):
 *   1. Platform-specific optional dependency  @zen/cli-<os>-<arch>
 *   2. Cached binary in ~/.zen/bin (downloaded once from GitHub Releases)
 *   3. Fresh download from GitHub Releases, SHA-256 verified against
 *      checksums.json shipped inside this package.
 *
 * All arguments are passed through to the Go binary verbatim; the child's
 * exit code becomes this process' exit code.
 */
'use strict';

const fs = require('fs');
const os = require('os');
const path = require('path');
const crypto = require('crypto');
const { spawnSync } = require('child_process');

const pkg = require('../package.json');

const PLATFORM_MAP = {
  'linux-x64': { slug: 'linux-x64', binary: 'zen' },
  'linux-arm64': { slug: 'linux-arm64', binary: 'zen' },
  'darwin-x64': { slug: 'darwin-x64', binary: 'zen' },
  'darwin-arm64': { slug: 'darwin-arm64', binary: 'zen' },
  'win32-x64': { slug: 'win32-x64', binary: 'zen.exe' },
};

const EXE_EXT = process.platform === 'win32' ? '.exe' : '';

function fail(msg) {
  console.error(`zen: ${msg}`);
  process.exit(1);
}

function platformKey() {
  const key = `${process.platform}-${process.arch}`;
  if (PLATFORM_MAP[key]) return key;
  fail(`unsupported platform ${process.platform}-${process.arch}. ` +
    'Supported: linux-x64, linux-arm64, darwin-x64, darwin-arm64, win32-x64.');
}

/** Try the @zen/cli-* optional dependency package. Returns bin path or null. */
function fromOptionalDep(key) {
  const slug = PLATFORM_MAP[key].slug;
  try {
    const pkgRoot = path.dirname(
      require.resolve(`${slug}/package.json`, { paths: [__dirname, process.cwd()] })
    );
    const bin = path.join(pkgRoot, 'bin', `zen${EXE_EXT}`);
    return fs.existsSync(bin) ? bin : null;
  } catch {
    return null;
  }
}

function checksums() {
  try {
    return require('../checksums.json');
  } catch {
    return {};
  }
}

function cachePath(version, key) {
  const dir = path.join(os.homedir(), '.zen', 'bin');
  const name = `zen-cli-v${version}-${key}${EXE_EXT}`;
  return { dir, bin: path.join(dir, name) };
}

function download(url, dest) {
  return fetch(url, { redirect: 'follow' }).then(async (res) => {
    if (!res.ok) throw new Error(`HTTP ${res.status} for ${url}`);
    const buf = Buffer.from(await res.arrayBuffer());
    fs.writeFileSync(dest, buf);
    return buf;
  });
}

function extractTarGz(tarPath, destDir) {
  const res = spawnSync('tar', ['-xzf', tarPath, '-C', destDir], { stdio: 'ignore' });
  if (res.status !== 0) throw new Error('tar extraction failed (is tar available?)');
}

/**
 * Resolve the binary through the cache, downloading from GitHub Releases on
 * cache miss. Returns the absolute path to the Go binary.
 */
async function ensureBinary(version, key) {
  const { dir, bin } = cachePath(version, key);
  if (fs.existsSync(bin)) return bin;

  const { repo, tagPrefix, releaseBaseUrl } = pkg.zen || {};
  if (!releaseBaseUrl && (!repo || repo.startsWith('YOUR_USERNAME'))) {
    fail(`this zen-cli package is not bound to a GitHub repo yet. Set "zen.repo" in ` +
      `the npm package, or install the matching @zen/cli-${PLATFORM_MAP[key].slug} package.`);
  }
  const tag = `${tagPrefix || 'zen-cli-v'}${version}`;
  const asset = `zen-cli-v${version}-${key}.tar.gz`;
  const base = pkg.zen.releaseBaseUrl || `https://github.com/${repo}/releases/download`;
  const url = `${base}/${tag}/${asset}`;

  fs.mkdirSync(dir, { recursive: true });
  process.stderr.write(`zen: downloading ${asset} from ${repo}…\n`);

  const tarPath = `${bin}.tar.gz`;
  let buf;
  try {
    buf = await download(url, tarPath);
  } catch (err) {
    fail(`download failed: ${err.message}`);
  }

  const expected = checksums()[asset];
  if (expected) {
    const actual = crypto.createHash('sha256').update(buf).digest('hex');
    if (actual !== expected) {
      fs.rmSync(tarPath, { force: true });
      fail(`checksum mismatch for ${asset}\n  want ${expected}\n  got  ${actual}`);
    }
  } else {
    console.error('zen: warning — no checksum entry for this asset, skipping verification');
  }

  try {
    extractTarGz(tarPath, dir);
    fs.rmSync(tarPath, { force: true });
  } catch (err) {
    fail(err.message);
  }
  const extracted = path.join(dir, `zen${EXE_EXT}`);
  if (!fs.existsSync(extracted)) fail('archive did not contain the zen binary');
  fs.renameSync(extracted, bin);
  fs.chmodSync(bin, 0o755);
  return bin;
}

async function main() {
  const version = pkg.version;
  const key = platformKey();

  let bin = fromOptionalDep(key);
  if (!bin) bin = await ensureBinary(version, key);

  const res = spawnSync(bin, process.argv.slice(2), {
    stdio: 'inherit',
    env: process.env,
    windowsHide: true,
  });

  if (res.error && res.error.code === 'ENOENT') {
    fail(`binary at ${bin} is missing or not executable`);
  }
  if (res.error) fail(`failed to launch zen: ${res.error.message}`);
  process.exit(res.status ?? 1);
}

main().catch((err) => fail(err && err.stack ? err.stack : String(err)));
