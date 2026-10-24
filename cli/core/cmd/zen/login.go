package zen

import (
	"os"
	"strings"

	"github.com/zen-ota/zen-cli/core/internal/config"
	"github.com/zen-ota/zen-cli/core/internal/github"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

func cmdLogin(args []string) int {
	fs := newFlagSet("login")
	token := fs.String("token", "", "GitHub personal access token (repo scope recommended)")
	api := fs.String("api", "", "Zen API base URL, e.g. https://zen-api.vercel.app")
	admin := fs.String("admin", "", "Zen admin token (the deployment's ZEN_ADMIN_TOKEN)")
	if code := parseFlags(fs, args, "store credentials in ~/.zen/config.json"); code >= 0 {
		return code
	}

	g, err := config.LoadGlobal()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	tok := strings.TrimSpace(*token)
	if tok == "" {
		if env := strings.TrimSpace(os.Getenv("ZEN_GITHUB_TOKEN")); env != "" {
			tok = env
			ui.Detail("using ZEN_GITHUB_TOKEN from the environment")
		}
	}
	if tok == "" {
		tok = ui.Prompt("GitHub token", g.GitHubToken)
	}
	if tok == "" {
		ui.Err("no GitHub token provided — create one at https://github.com/settings/tokens")
		return 1
	}

	apiURL := strings.TrimSpace(*api)
	if apiURL == "" {
		apiURL = ui.Prompt("Zen API base URL (empty to skip)", g.APIBaseURL)
	}
	adminTok := strings.TrimSpace(*admin)
	if adminTok == "" {
		adminTok = ui.Prompt("Zen admin token (empty to skip)", g.AdminToken)
	}

	login, scopes, err := github.NewClient(tok).GetUser()
	if err != nil {
		ui.Err("could not verify the token with GitHub: %v (nothing was saved)", err)
		return 1
	}
	ui.Success("authenticated as %s", login)
	if !strings.Contains(scopes, "repo") {
		ui.Warn("token scopes (%q) do not include 'repo' — private repos and asset uploads may fail", scopes)
	}

	g.GitHubToken = tok
	g.APIBaseURL = apiURL
	g.AdminToken = adminTok
	if err := config.SaveGlobal(g); err != nil {
		ui.Err("save config: %v", err)
		return 1
	}
	if path, perr := config.GlobalPath(); perr == nil {
		ui.Success("saved %s", path)
	}
	ui.Detail("next: cd into a project and run 'zen init'")
	return 0
}

func cmdWhoami(args []string) int {
	fs := newFlagSet("whoami")
	if code := parseFlags(fs, args, "show the stored credentials"); code >= 0 {
		return code
	}
	g, err := config.LoadGlobal()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	login := "unknown"
	if g.GitHubToken != "" {
		if l, _, lerr := github.NewClient(g.GitHubToken).GetUser(); lerr == nil && l != "" {
			login = l
		}
	}
	admin := "unset"
	if g.AdminToken != "" {
		admin = "set"
	}
	apiURL := g.APIBaseURL
	if apiURL == "" {
		apiURL = "unset"
	}
	r2State := "not configured"
	if g.R2AccountID != "" && g.R2AccessKeyID != "" && g.R2SecretAccessKey != "" && g.R2Bucket != "" {
		r2State = "configured"
	}

	t := ui.NewTable("field", "value")
	t.AddRow("github token", maskOrDefault(g.GitHubToken))
	t.AddRow("github login", login)
	t.AddRow("apiBaseUrl", apiURL)
	t.AddRow("adminToken", admin)
	t.AddRow("r2", r2State)
	t.Print()
	return 0
}

func maskOrDefault(tok string) string {
	if strings.TrimSpace(tok) == "" {
		return "(unset)"
	}
	return config.MaskToken(tok)
}
