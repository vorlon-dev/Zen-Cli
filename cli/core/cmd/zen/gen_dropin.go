package zen

import (
	"os"

	"github.com/zen-ota/zen-cli/core/internal/assets"
	"github.com/zen-ota/zen-cli/core/internal/ui"
)

func cmdGenDropin(args []string) int {
	fs := newFlagSet("gen-dropin")
	force := fs.Bool("force", false, "overwrite an existing ZenUpdater.kt")
	if code := parseFlags(fs, args, "write the ZenUpdater.kt drop-in into the current directory"); code >= 0 {
		return code
	}

	const out = "ZenUpdater.kt"
	if _, err := os.Stat(out); err == nil && !*force {
		ui.Warn("%s already exists — pass --force to overwrite", out)
		return 0
	}
	data := assets.ZenUpdaterKt()
	if len(data) == 0 {
		ui.Err("embedded ZenUpdater.kt is empty — this build is broken")
		return 1
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		ui.Err("write %s: %v", out, err)
		return 1
	}
	ui.Success("wrote %s (%d bytes)", out, len(data))
	ui.Detail("wire it up: instantiate ZenUpdater from your Application class with the API base URL and appId")
	return 0
}
