package zen

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zen-ota/zen-cli/core/internal/api"
	"github.com/zen-ota/zen-cli/core/internal/apk"
	"github.com/zen-ota/zen-cli/core/internal/config"
	"github.com/zen-ota/zen-cli/core/internal/github"
	"github.com/zen-ota/zen-cli/core/internal/manifest"
	"github.com/zen-ota/zen-cli/core/internal/r2"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

const publishSummary = "publish an APK to a channel (GitHub release + manifest commit)"

func cmdPublish(args []string) int {
	fs := newFlagSet("publish")
	channel := fs.String("channel", "stable", "target channel: stable | beta | alpha")
	rollout := fs.Int("rollout", 100, "staged rollout percentage (0-100)")
	force := fs.Bool("force", false, "set minVersionCode to this versionCode (forces updates below it)")
	minVersionCode := fs.Int64("min-version-code", -1, "forced-update floor; -1 carries the channel's current floor forward")
	notes := fs.String("notes", "", "release notes (plain text or markdown)")
	notesFile := fs.String("notes-file", "", "read release notes from a file (overrides --notes)")
	useR2 := fs.Bool("r2", false, "also mirror the APK to Cloudflare R2")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	dryRun := fs.Bool("dry-run", false, "print the plan and exit without changing anything")
	forcePublish := fs.Bool("force-publish", false, "allow publishing a versionCode lower than the channel's current one")
	if code := parseFlags(fs, args, publishSummary); code >= 0 {
		return code
	}

	// 1. project + global config
	proj, err := loadProjectConfig()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}
	g, err := loadGlobalConfig()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	// 2. locate and parse the APK
	if fs.NArg() < 1 {
		ui.Err("missing APK path — usage: zen publish [flags] <path/to/app.apk>")
		return 1
	}
	apkPath, err := resolveAPKPath(fs.Arg(0))
	if err != nil {
		ui.Err("%v", err)
		return 1
	}
	var info *apk.Manifest
	var perr error
	if err := ui.PrintStep("parsing "+apkPath, func() error {
		info, perr = apk.ParseAPK(apkPath)
		return perr
	}); err != nil {
		return 1
	}
	if info.Package != proj.AppID {
		ui.Err("APK package %q does not match appId %q from zen.config.json — fix zen.config.json or rebuild the APK", info.Package, proj.AppID)
		return 1
	}
	if info.VersionCode <= 0 {
		ui.Err("APK has versionCode %d — it must be a positive integer", info.VersionCode)
		return 1
	}

	// 3. channel whitelist + rollout range
	allowed := false
	for _, ch := range proj.Channels {
		if ch == *channel {
			allowed = true
			break
		}
	}
	if !allowed {
		ui.Err("channel %q is not enabled for this project (zen.config.json channels: %s)", *channel, strings.Join(proj.Channels, " "))
		return 1
	}
	if *rollout < 0 || *rollout > 100 {
		ui.Err("--rollout must be between 0 and 100, got %d", *rollout)
		return 1
	}

	// 4. current manifest (admin API → raw GitHub → Contents API → fresh)
	ui.Detail("fetching current manifest…")
	current, err := fetchManifest(proj, g)
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	// 5. versionCode monotonicity (contract §8)
	versionCode := info.VersionCode
	cur, hasCurrent := current.Current(*channel)
	if hasCurrent && versionCode < cur.VersionCode {
		if !*forcePublish {
			ui.Err("versionCode %d is lower than the current %s release (versionCode %d) — pass --force-publish to publish a rollback-style release", versionCode, *channel, cur.VersionCode)
			return 1
		}
		if !*yes {
			ok := ui.Confirm(fmt.Sprintf("Publish versionCode %d below the current %s versionCode %d?", versionCode, *channel, cur.VersionCode))
			if !ok {
				ui.Detail("aborted")
				return 1
			}
		}
	}

	// 6. minVersionCode resolution: explicit flag > --force > carry forward
	minCode := int64(0)
	if hasCurrent {
		minCode = cur.MinVersionCode
	}
	switch {
	case *minVersionCode >= 0:
		minCode = *minVersionCode
	case *force:
		minCode = versionCode
	}

	// 7. release notes
	noteText := strings.TrimSpace(*notes)
	if *notesFile != "" {
		data, rerr := os.ReadFile(*notesFile)
		if rerr != nil {
			ui.Err("read --notes-file: %v", rerr)
			return 1
		}
		noteText = strings.TrimSpace(string(data))
	}

	// 8. hash + size
	var sha string
	if err := ui.PrintStep("computing sha256", func() error {
		var herr error
		sha, herr = apk.FileSHA256(apkPath)
		return herr
	}); err != nil {
		ui.Err("%v", err)
		return 1
	}
	size, err := apk.FileSize(apkPath)
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	tag := fmt.Sprintf("zen-%s-v%s", *channel, info.VersionName)
	assetName := fmt.Sprintf("%s-%s-v%s.apk", proj.APKAssetPrefix, *channel, info.VersionName)

	// 9. dry run — print the plan, change nothing
	if *dryRun {
		printPublishPlan(proj, g, apkPath, info, *channel, *rollout, minCode, sha, size, tag, assetName, noteText, *useR2, hasCurrent, cur)
		return 0
	}

	// 10. create (or reuse) the release
	gh := github.NewClient(g.GitHubToken)
	var rel *github.Release
	if err := ui.PrintStep("release "+tag, func() error {
		var rerr error
		rel, rerr = gh.CreateRelease(proj.Repo, tag, fmt.Sprintf("Zen %s v%s", *channel, info.VersionName), noteText)
		return rerr
	}); err != nil {
		ui.Err("%v", err)
		return 1
	}

	// 11. replace an existing same-name asset (contract §6)
	for _, a := range rel.Assets {
		if a.Name == assetName {
			if err := ui.PrintStep("deleting existing asset "+assetName, func() error {
				return gh.DeleteAsset(a.URL)
			}); err != nil {
				ui.Warn("could not delete the old asset (continuing): %v", err)
			}
			break
		}
	}
	var downloadURL string
	if err := ui.PrintStep(fmt.Sprintf("uploading %s (%s)", assetName, humanSize(size)), func() error {
		var uerr error
		downloadURL, uerr = gh.UploadAsset(proj.Repo, rel.UploadURL, tag, assetName, apkPath)
		return uerr
	}); err != nil {
		ui.Err("%v", err)
		ui.Detail("tip: re-run the same command — the existing asset is replaced automatically")
		return 1
	}

	// 12. optional R2 mirror
	r2URL := ""
	if *useR2 {
		if !r2Ready(g) {
			ui.Warn("--r2 requested but R2 credentials are incomplete in ~/.zen/config.json (r2AccountId, r2AccessKeyId, r2SecretAccessKey, r2Bucket, r2PublicBase) — skipping")
		} else {
			key := proj.AppID + "/" + assetName
			if err := ui.PrintStep("uploading to R2", func() error {
				var rerr error
				r2URL, rerr = r2.Put(g.R2Bucket, g.R2PublicBase, g.R2AccountID, g.R2AccessKeyID, g.R2SecretAccessKey, key, apkPath)
				return rerr
			}); err != nil {
				ui.Warn("R2 upload failed (continuing with GitHub only): %v", err)
				r2URL = ""
			}
		}
	}

	// 13. manifest entry
	current.Upsert(*channel, manifest.Release{
		VersionName:    info.VersionName,
		VersionCode:    versionCode,
		SHA256:         sha,
		Size:           size,
		ReleaseNotes:   noteText,
		PublishedAt:    time.Now().UTC().Format(time.RFC3339),
		Rollout:        *rollout,
		MinVersionCode: minCode,
		Assets:         manifest.Assets{GitHub: downloadURL, R2: r2URL},
	})
	pretty, err := current.MarshalPretty()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	// 14. commit the manifest (fatal on failure — the release exists already)
	if err := ui.PrintStep(fmt.Sprintf("committing %s to %s", proj.ManifestPath, proj.Branch), func() error {
		return gh.PutContents(proj.Repo, proj.ManifestPath, proj.Branch,
			fmt.Sprintf("zen: publish v%s (code %d) to %s", info.VersionName, versionCode, *channel), pretty)
	}); err != nil {
		ui.Err("manifest commit failed: %v", err)
		ui.Detail("the GitHub release was created — check branch protection, then re-run or commit the manifest manually")
		return 1
	}

	// 15. admin API mirror (best effort, contract §5)
	if g.AdminToken != "" && proj.APIBaseURL != "" {
		if err := ui.PrintStep("mirroring manifest to the admin API", func() error {
			_, aerr := api.PostManifest(proj.APIBaseURL, g.AdminToken, current)
			return aerr
		}); err != nil {
			ui.Warn("admin API mirror failed (non-fatal): %v", err)
		}
	}

	// 16. summary
	ui.Success("published %s v%s (versionCode %d) to %s", proj.AppID, info.VersionName, versionCode, *channel)
	t := ui.NewTable("field", "value")
	t.AddRow("release", fmt.Sprintf("https://github.com/%s/releases/tag/%s", proj.Repo, tag))
	t.AddRow("asset", downloadURL)
	if r2URL != "" {
		t.AddRow("r2", r2URL)
	}
	t.AddRow("manifest", fmt.Sprintf("%s @ %s", proj.ManifestPath, proj.Branch))
	t.AddRow("rollout", fmt.Sprintf("%d%% (minVersionCode %d)", *rollout, minCode))
	t.Print()
	hint := fmt.Sprintf("%s/api/check?appId=%s&channel=%s&versionCode=0&deviceId=zen-smoke", proj.APIBaseURL, proj.AppID, *channel)
	if proj.APIBaseURL == "" {
		hint = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", proj.Repo, proj.Branch, proj.ManifestPath)
	}
	ui.Detail("smoke test:")
	ui.Detail("  curl -s %q", hint)
	return 0
}

func printPublishPlan(proj *config.Project, g *config.Global, apkPath string, info *apk.Manifest, channel string, rollout int, minCode int64, sha string, size int64, tag, assetName, notes string, useR2 bool, hasCurrent bool, cur *manifest.Release) {
	t := ui.NewTable("plan", "value")
	t.AddRow("repo", fmt.Sprintf("%s (branch %s)", proj.Repo, proj.Branch))
	t.AddRow("channel", channel)
	t.AddRow("apk", fmt.Sprintf("%s (%s, %s)", apkPath, info.Package, humanSize(size)))
	t.AddRow("version", fmt.Sprintf("%s (versionCode %d)", info.VersionName, info.VersionCode))
	t.AddRow("sha256", sha)
	t.AddRow("tag", tag)
	t.AddRow("asset", assetName)
	t.AddRow("rollout", fmt.Sprintf("%d%%", rollout))
	t.AddRow("minVersionCode", strconv.FormatInt(minCode, 10))
	t.AddRow("notes", firstLine(notes))
	t.AddRow("manifest", fmt.Sprintf("%s @ %s", proj.ManifestPath, proj.Branch))
	t.AddRow("admin api", adminTarget(proj, g))
	t.AddRow("r2 mirror", map[bool]string{true: "planned", false: "no"}[useR2])
	if hasCurrent {
		t.AddRow("current", fmt.Sprintf("v%s (versionCode %d)", cur.VersionName, cur.VersionCode))
	}
	t.Print()
	fmt.Println("(dry run — nothing was written)")
}

func resolveAPKPath(p string) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("cannot access %s: %w", p, err)
	}
	if !st.IsDir() {
		return p, nil
	}
	newest := apk.NewestAPK(p)
	if newest == "" {
		return "", fmt.Errorf("no *.apk files found under %s", p)
	}
	return newest, nil
}

func r2Ready(g *config.Global) bool {
	return g.R2AccountID != "" && g.R2AccessKeyID != "" && g.R2SecretAccessKey != "" && g.R2Bucket != "" && g.R2PublicBase != ""
}

func adminTarget(proj *config.Project, g *config.Global) string {
	if g.AdminToken != "" && proj.APIBaseURL != "" {
		return proj.APIBaseURL
	}
	return "(not configured — instant propagation disabled)"
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}
