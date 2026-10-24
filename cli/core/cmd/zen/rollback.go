package zen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zen-ota/zen-cli/core/internal/manifest"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

func cmdRollback(args []string) int {
	fs := newFlagSet("rollback")
	channel := fs.String("channel", "stable", "channel to roll back")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if code := parseFlags(fs, args, "make an older release current again"); code >= 0 {
		return code
	}
	if fs.NArg() < 1 {
		ui.Err("usage: zen rollback <versionName|versionCode> [--channel stable]")
		return 1
	}
	target := fs.Arg(0)

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
	m, err := fetchManifest(proj, g)
	if err != nil {
		ui.Err("%v", err)
		return 1
	}
	if _, ok := m.Channels[*channel]; !ok {
		ui.Err("channel %s has no releases yet", *channel)
		return 1
	}

	var code int64
	if n, perr := strconv.ParseInt(target, 10, 64); perr == nil {
		code = n
	} else {
		rel, found := m.FindByName(*channel, target)
		if !found {
			ui.Err("no release %q in channel %s — run 'zen list --channel %s'", target, *channel, *channel)
			return 1
		}
		code = rel.VersionCode
	}
	rel, found := m.Find(*channel, code)
	if !found {
		ui.Err("no release with versionCode %d in channel %s (published: %s)", code, *channel, channelCodes(m, *channel))
		return 1
	}
	if cur, ok := m.Current(*channel); ok && cur.VersionCode == code {
		ui.Err("v%s (versionCode %d) is already the current release on %s", rel.VersionName, code, *channel)
		return 1
	}
	if !*yes {
		if !ui.Confirm(fmt.Sprintf("Roll back %s to v%s (versionCode %d)?", *channel, rel.VersionName, code)) {
			ui.Detail("aborted")
			return 1
		}
	}
	if err := m.RollbackTo(*channel, code); err != nil {
		ui.Err("%v", err)
		return 1
	}
	msg := fmt.Sprintf("zen: roll back %s to v%s (code %d)", *channel, rel.VersionName, code)
	if err := commitManifest(proj, g, m, msg); err != nil {
		ui.Err("manifest commit failed: %v", err)
		return 1
	}
	cur, _ := m.Current(*channel)
	ui.Success("channel %s now serves v%s (versionCode %d, rollout %d%%)", *channel, cur.VersionName, cur.VersionCode, cur.Rollout)
	return 0
}

func channelCodes(m *manifest.Manifest, ch string) string {
	h, ok := m.Channels[ch]
	if !ok {
		return "(none)"
	}
	var sb strings.Builder
	for i, r := range h.History {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "v%s(%d)", r.VersionName, r.VersionCode)
	}
	return sb.String()
}
