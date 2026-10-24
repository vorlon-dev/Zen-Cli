package zen

import (
	"fmt"
	"strconv"

	"github.com/zen-ota/zen-cli/core/internal/ui"
)

func cmdPromote(args []string) int {
	fs := newFlagSet("promote")
	from := fs.String("from", "", "source channel (required)")
	to := fs.String("to", "", "target channel (required)")
	yes := fs.Bool("yes", false, "do not ask for confirmation")
	if code := parseFlags(fs, args, "copy a release from one channel to another"); code >= 0 {
		return code
	}
	if fs.NArg() < 1 || *from == "" || *to == "" {
		ui.Err("usage: zen promote <versionName|versionCode> --from beta --to stable")
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

	var code int64
	if n, perr := strconv.ParseInt(target, 10, 64); perr == nil {
		code = n
	} else {
		rel, found := m.FindByName(*from, target)
		if !found {
			ui.Err("no release %q in channel %s — run 'zen list --channel %s'", target, *from, *from)
			return 1
		}
		code = rel.VersionCode
	}
	rel, found := m.Find(*from, code)
	if !found {
		ui.Err("no release with versionCode %d in channel %s (published: %s)", code, *from, channelCodes(m, *from))
		return 1
	}
	if !*yes {
		if !ui.Confirm(fmt.Sprintf("Promote v%s (versionCode %d) from %s to %s?", rel.VersionName, code, *from, *to)) {
			ui.Detail("aborted")
			return 1
		}
	}
	if err := m.Promote(*from, *to, code); err != nil {
		ui.Err("%v", err)
		return 1
	}
	msg := fmt.Sprintf("zen: promote v%s (code %d) from %s to %s", rel.VersionName, code, *from, *to)
	if err := commitManifest(proj, g, m, msg); err != nil {
		ui.Err("manifest commit failed: %v", err)
		return 1
	}
	cur, _ := m.Current(*to)
	ui.Success("%s now serves v%s (versionCode %d, rollout %d%%)", *to, cur.VersionName, cur.VersionCode, cur.Rollout)
	return 0
}
