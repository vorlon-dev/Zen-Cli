package zen

import "fmt"

// Version is the CLI version reported by `zen version`.
const Version = "1.0.0"

func cmdVersion(args []string) int {
	fmt.Printf("zen version %s\n", Version)
	return 0
}
