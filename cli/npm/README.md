# zen-cli (npm)

The npm distribution of the Zen CLI. Installing it gives you the `zen` command —
a thin Node dispatcher that runs the fast Go core binary.

```bash
npm install -g zen-cli
zen version
```

## How the binary resolves

1. **Platform package** — `@zen/cli-<os>-<arch>` optional dependency (published
   alongside the main package, e.g. `@zen/cli-linux-x64`).
2. **GitHub Releases** — on first run the dispatcher downloads
   `zen-cli-v<version>-<os>-<arch>.tar.gz` from the release tagged
   `zen-cli-v<version>`, verifies its SHA-256 against the bundled
   `checksums.json`, extracts, caches it in `~/.zen/bin/`, and reuses it forever.

Both paths are automatic; no postinstall scripts run.

## Publishing your own fork

1. Fork this repo. Push a tag: `git tag zen-cli-v1.0.1 && git push origin zen-cli-v1.0.1`
   — the `release-cli.yml` workflow builds all five platforms and attaches the
   archives + `checksums.json` to the GitHub release.
2. Edit `cli/npm/package.json`:
   - set `"zen.repo"` to `<you>/<your-repo>`
   - bump `"version"`
3. `cd cli/npm && npm publish --access public` (or flip `npm_publish` on in the
   workflow dispatch).

If you prefer to publish five platform packages instead of the download flow,
create `@zen/cli-<os>-<arch>` packages each containing the extracted binary at
`bin/zen[.exe]` — the dispatcher picks them up automatically.

## Requirements

- Node ≥ 18 (for `fetch`)
- `tar` on PATH (present by default on Linux, macOS, Windows 10+)
