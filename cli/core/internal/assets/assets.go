// Package assets embeds the drop-in SDK file served by `zen gen-dropin`.
// The build pipeline replaces the placeholder with the real ZenUpdater.kt
// from android/dropin before release.
package assets

import (
	"embed"
	"io/fs"
)

//go:embed ZenUpdater.kt
var dropinFS embed.FS

// DropinFS exposes the embedded drop-in assets as a read-only filesystem.
func DropinFS() fs.FS { return dropinFS }

// ZenUpdaterKt returns the raw bytes of the embedded ZenUpdater.kt.
func ZenUpdaterKt() []byte {
	b, err := fs.ReadFile(dropinFS, "ZenUpdater.kt")
	if err != nil {
		return nil
	}
	return b
}
