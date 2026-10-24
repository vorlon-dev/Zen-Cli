package zen

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/zen-ota/zen-cli/core/internal/apk"
	"github.com/zen-ota/zen-cli/core/internal/config"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

func cmdInit(args []string) int {
	fs := newFlagSet("init")
	appID := fs.String("app-id", "", "application id, e.g. com.example.app (non-interactive)")
	repo := fs.String("repo", "", "GitHub repo as owner/repo (non-interactive)")
	branch := fs.String("branch", "", "branch the manifest is committed to (default: detected or main)")
	api := fs.String("api", "", "Zen API base URL (non-interactive)")
	if code := parseFlags(fs, args, "create zen.config.json for this project"); code >= 0 {
		return code
	}

	if _, err := os.Stat(config.FileName); err == nil {
		if !ui.Confirm(config.FileName + " already exists — overwrite?") {
			ui.Detail("aborted")
			return 0
		}
	}

	// repo: flag > git remote origin > prompt
	repoStr := strings.TrimSpace(*repo)
	if repoStr == "" {
		if detected := detectGitRepo(); detected != "" {
			repoStr = detected
			ui.Detail("detected repo from git remote origin: %s", repoStr)
		}
	}
	if repoStr == "" {
		repoStr = ui.Prompt("GitHub repo (owner/repo)", "")
	}
	if _, _, err := config.OwnerRepo(repoStr); err != nil {
		ui.Err("%v", err)
		return 1
	}

	// appId: flag > newest APK in cwd > prompt
	appIDStr := strings.TrimSpace(*appID)
	if newest := apk.NewestAPK("."); newest != "" && appIDStr == "" {
		if m, perr := apk.ParseAPK(newest); perr == nil && m.Package != "" {
			ui.Detail("detected appId from %s: %s", newest, m.Package)
			appIDStr = m.Package
		}
	}
	if appIDStr == "" {
		appIDStr = ui.Prompt("App ID", appIDStr)
	}
	if appIDStr == "" {
		ui.Err("appId is required")
		return 1
	}

	// branch: flag > git > prompt (default main)
	branchStr := strings.TrimSpace(*branch)
	if branchStr == "" {
		branchStr = detectGitBranch()
	}
	branchStr = ui.Prompt("Branch to publish from", branchStr)

	apiStr := strings.TrimSpace(*api)
	if apiStr == "" {
		apiStr = ui.Prompt("Zen API base URL (empty to configure later)", "")
	}

	p := &config.Project{
		AppID:      appIDStr,
		Repo:       repoStr,
		Branch:     branchStr,
		APIBaseURL: apiStr,
	}
	if err := config.SaveProject(config.FileName, p); err != nil {
		ui.Err("write %s: %v", config.FileName, err)
		return 1
	}
	ui.Success("wrote %s", config.FileName)
	fmt.Print(`
Next steps:
  1. Add the OTA SDK to the app:
       drop-in:  zen gen-dropin   → copy ZenUpdater.kt, init from your Application class
       library:  include the zen-android Gradle module
  2. Deploy the API (zen-ota/api/) to Vercel, then:
       zen login --api https://<deployment> --admin <ZEN_ADMIN_TOKEN>
  3. Publish the first release:
       zen publish app/build/outputs/apk/release/app-release.apk
`)
	return 0
}

// detectGitRepo returns owner/repo parsed from the git remote origin, or ""
// when git is missing or the remote is not GitHub.
func detectGitRepo() string {
	if out, err := exec.Command("git", "remote", "get-url", "origin").Output(); err == nil {
		if r := parseGitURL(string(out)); r != "" {
			return r
		}
	}
	if out, err := exec.Command("git", "config", "--get", "remote.origin.url").Output(); err == nil {
		return parseGitURL(string(out))
	}
	return ""
}

// parseGitURL understands https://github.com/X/Y(.git), ssh://git@github.com/
// X/Y.git and git@github.com:X/Y.git.
func parseGitURL(raw string) string {
	u := strings.TrimSpace(raw)
	u = strings.TrimSuffix(u, ".git")
	u = strings.TrimSuffix(u, "/")
	switch {
	case strings.HasPrefix(u, "https://github.com/"):
		u = strings.TrimPrefix(u, "https://github.com/")
	case strings.HasPrefix(u, "http://github.com/"):
		u = strings.TrimPrefix(u, "http://github.com/")
	case strings.HasPrefix(u, "ssh://git@github.com/"):
		u = strings.TrimPrefix(u, "ssh://git@github.com/")
	case strings.HasPrefix(u, "git@github.com:"):
		u = strings.TrimPrefix(u, "git@github.com:")
	default:
		return ""
	}
	return strings.Trim(u, "/")
}

func detectGitBranch() string {
	if out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		if b := strings.TrimSpace(string(out)); b != "" && b != "HEAD" {
			return b
		}
	}
	return "main"
}
