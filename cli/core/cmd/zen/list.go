package zen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zen-ota/zen-cli/core/internal/config"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

func cmdList(args []string) int {
	fs := newFlagSet("list")
	channel := fs.String("channel", "", "show only this channel")
	asJSON := fs.Bool("json", false, "print the raw manifest JSON")
	if code := parseFlags(fs, args, "list published releases per channel"); code >= 0 {
		return code
	}

	proj, err := loadProjectConfig()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}
	g, err := config.LoadGlobal()
	if err != nil {
		ui.Err("%v", err)
		return 1
	}
	m, err := fetchManifest(proj, g)
	if err != nil {
		ui.Err("%v", err)
		return 1
	}

	if *asJSON {
		pretty, jerr := m.MarshalPretty()
		if jerr != nil {
			ui.Err("%v", jerr)
			return 1
		}
		fmt.Print(string(pretty))
		return 0
	}

	channels := m.ChannelNames()
	if *channel != "" {
		channels = []string{*channel}
	}
	any := false
	for _, ch := range channels {
		h, ok := m.Channels[ch]
		if !ok || len(h.History) == 0 {
			if *channel != "" {
				ui.Warn("channel %s has no releases yet", ch)
			}
			continue
		}
		any = true
		fmt.Println(ui.Bold(ch))
		t := ui.NewTable("version", "code", "rollout", "sha256", "published", "notes")
		for _, r := range h.History {
			t.AddRow(r.VersionName,
				strconv.FormatInt(r.VersionCode, 10),
				fmt.Sprintf("%d%%", r.Rollout),
				shortSHA(r.SHA256),
				shortDate(r.PublishedAt),
				firstLine(r.ReleaseNotes))
		}
		t.Print()
		fmt.Println()
	}
	if !any && *channel == "" {
		ui.Detail("no releases published yet — run 'zen publish <app.apk>'")
	}
	return 0
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}

func shortDate(rfc3339 string) string {
	return strings.SplitN(rfc3339, "T", 2)[0]
}
