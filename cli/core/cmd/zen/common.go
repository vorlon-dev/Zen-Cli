package zen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zen-ota/zen-cli/core/internal/api"
	"github.com/zen-ota/zen-cli/core/internal/config"
	"github.com/zen-ota/zen-cli/core/internal/github"
	"github.com/zen-ota/zen-cli/core/internal/manifest"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

// loadProjectConfig loads ./zen.config.json with a friendly error.
func loadProjectConfig() (*config.Project, error) {
	if _, err := os.Stat(config.FileName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("not a zen project — run `zen init`")
		}
		return nil, err
	}
	return config.LoadProject(config.FileName)
}

// loadGlobalConfig loads ~/.zen/config.json and requires a GitHub token.
func loadGlobalConfig() (*config.Global, error) {
	g, err := config.LoadGlobal()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(g.GitHubToken) == "" {
		return nil, errors.New("no GitHub token — run `zen login` first")
	}
	return g, nil
}

// fetchManifest resolves the current release manifest (contract §5): the
// admin API when configured, else raw.githubusercontent.com, with the
// GitHub Contents API as a fallback for private repos (raw returns 404 for
// those). A missing manifest yields a fresh one for the appId.
func fetchManifest(proj *config.Project, g *config.Global) (*manifest.Manifest, error) {
	if g != nil && g.AdminToken != "" && proj.APIBaseURL != "" {
		raw, err := api.GetManifest(proj.APIBaseURL, g.AdminToken, proj.AppID)
		if err == nil {
			return manifest.Load(raw)
		}
		ui.Warn("admin API manifest fetch failed (%v) — falling back to GitHub", err)
	}

	raw, found, rerr := fetchRawManifest(proj.Repo, proj.Branch, proj.ManifestPath)
	if rerr == nil && found {
		return manifest.Load(raw)
	}
	if rerr != nil {
		ui.Warn("raw manifest fetch failed (%v) — trying the GitHub Contents API", rerr)
	}
	if g != nil && g.GitHubToken != "" {
		con, cerr := github.NewClient(g.GitHubToken).GetContents(proj.Repo, proj.ManifestPath, proj.Branch)
		if cerr == nil {
			return manifest.Load(con.Content)
		}
		if !github.IsNotFound(cerr) {
			return nil, fmt.Errorf("fetch %s from %s: %w", proj.ManifestPath, proj.Repo, cerr)
		}
	} else if rerr != nil {
		return nil, fmt.Errorf("fetch manifest: %w", rerr)
	}
	return manifest.New(proj.AppID), nil
}

// fetchRawManifest fetches the manifest from raw.githubusercontent.com.
// found=false without error means HTTP 404 (no manifest yet).
func fetchRawManifest(repo, branch, path string) ([]byte, bool, error) {
	u := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, branch, path)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return data, true, nil
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
}

// commitManifest writes the manifest to GitHub (Contents API) and mirrors
// it to the admin API best-effort (warn only, per contract §5).
func commitManifest(proj *config.Project, g *config.Global, m *manifest.Manifest, message string) error {
	pretty, err := m.MarshalPretty()
	if err != nil {
		return err
	}
	if err := github.NewClient(g.GitHubToken).PutContents(proj.Repo, proj.ManifestPath, proj.Branch, message, pretty); err != nil {
		return err
	}
	if g.AdminToken != "" && proj.APIBaseURL != "" {
		if _, err := api.PostManifest(proj.APIBaseURL, g.AdminToken, m); err != nil {
			ui.Warn("admin API mirror failed (non-fatal): %v", err)
		} else {
			ui.Detail("manifest mirrored to the admin API")
		}
	}
	return nil
}
