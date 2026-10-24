// Package config loads and saves the two configuration files the Zen CLI
// uses: the per-user global config (~/.zen/config.json, chmod 0600) and the
// per-project zen.config.json. All writes are atomic (tmp + rename).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zen-ota/zen-cli/core/internal/atomicfile"
)

// FileName is the project configuration file name.
const FileName = "zen.config.json"

// DefaultManifestPath is where the release manifest is committed in the repo.
const DefaultManifestPath = ".zen/manifest.json"

// DefaultChannels is the canonical channel list (contract §1).
var DefaultChannels = []string{"stable", "beta", "alpha"}

// Global holds the credentials stored in ~/.zen/config.json.
type Global struct {
	GitHubToken       string `json:"githubToken"`
	APIBaseURL        string `json:"apiBaseUrl"`
	AdminToken        string `json:"adminToken"`
	R2AccountID       string `json:"r2AccountId"`
	R2AccessKeyID     string `json:"r2AccessKeyId"`
	R2SecretAccessKey string `json:"r2SecretAccessKey"`
	R2Bucket          string `json:"r2Bucket"`
	R2PublicBase      string `json:"r2PublicBase"`
}

// GlobalDir returns ~/.zen.
func GlobalDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return home + "/.zen", nil
}

// GlobalPath returns ~/.zen/config.json.
func GlobalPath() (string, error) {
	dir, err := GlobalDir()
	if err != nil {
		return "", err
	}
	return dir + "/config.json", nil
}

// LoadGlobal reads the global config. A missing file yields an empty config,
// not an error — first-run is a normal state.
func LoadGlobal() (*Global, error) {
	path, err := GlobalPath()
	if err != nil {
		return nil, err
	}
	g := &Global{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return g, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, g); err != nil {
		return nil, fmt.Errorf("%s is not valid json: %w", path, err)
	}
	return g, nil
}

// SaveGlobal writes the global config atomically with chmod 0600.
func SaveGlobal(g *Global) error {
	dir, err := GlobalDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	return atomicfile.Write(dir+"/config.json", data, 0o600)
}

// Project describes one app, mirroring the contract §5 zen.config.json.
type Project struct {
	AppID          string   `json:"appId"`
	Repo           string   `json:"repo"`
	Branch         string   `json:"branch"`
	APIBaseURL     string   `json:"apiBaseUrl,omitempty"`
	ManifestPath   string   `json:"manifestPath"`
	Channels       []string `json:"channels"`
	APKAssetPrefix string   `json:"apkAssetPrefix"`
}

// Normalize fills in defaults for any empty fields.
func (p *Project) Normalize() {
	p.AppID = strings.TrimSpace(p.AppID)
	p.Repo = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p.Repo), ".git"))
	p.APIBaseURL = strings.TrimSpace(p.APIBaseURL)
	if p.Branch == "" {
		p.Branch = "main"
	}
	if p.ManifestPath == "" {
		p.ManifestPath = DefaultManifestPath
	}
	if len(p.Channels) == 0 {
		p.Channels = append([]string(nil), DefaultChannels...)
	}
	if p.APKAssetPrefix == "" {
		p.APKAssetPrefix = "app"
	}
}

// Validate reports user-actionable problems with the project config.
func (p *Project) Validate() error {
	if p.AppID == "" {
		return errors.New("appId is empty")
	}
	if strings.ContainsAny(p.AppID, " \t/") {
		return fmt.Errorf("appId %q contains invalid characters", p.AppID)
	}
	if _, _, err := OwnerRepo(p.Repo); err != nil {
		return err
	}
	if p.Branch == "" {
		return errors.New("branch is empty")
	}
	return nil
}

// OwnerRepo splits an "owner/repo" string, tolerating a trailing ".git".
func OwnerRepo(repo string) (string, string, error) {
	repo = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(repo), ".git"))
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo %q: expected \"owner/repo\"", repo)
	}
	return parts[0], parts[1], nil
}

// LoadProject reads and validates zen.config.json from path.
func LoadProject(path string) (*Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found — run 'zen init' to create it", path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	p := &Project{}
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("%s is not valid json: %w", path, err)
	}
	p.Normalize()
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// SaveProject writes zen.config.json atomically (chmod 0644, pretty JSON).
func SaveProject(path string, p *Project) error {
	p.Normalize()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("encode project config: %w", err)
	}
	data = append(data, '\n')
	return atomicfile.Write(path, data, 0o644)
}

// MaskToken shortens a secret for display: first 6 characters + "...".
func MaskToken(tok string) string {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return ""
	}
	if len(tok) <= 6 {
		return strings.Repeat("*", len(tok))
	}
	return tok[:6] + "..."
}
