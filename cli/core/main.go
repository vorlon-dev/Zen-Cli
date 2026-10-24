// Command zen is the entry point of the Zen OTA command line interface.
// All logic lives in cmd/zen; this file stays intentionally thin.
package main

import (
	"os"

	"github.com/zen-ota/zen-cli/core/cmd/zen"
)

func main() {
	os.Exit(zen.Execute())
}
