// Package zen implements the Zen OTA CLI commands. root.go holds the
// dispatcher, the usage screen and the shared flag-parsing helpers.
package zen

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// keepGoing is the parseFlags sentinel meaning "continue with the command".
const keepGoing = -1

// Execute dispatches os.Args[1] to the subcommands and returns the process
// exit code.
func Execute() int {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "zen: missing command")
		usage()
		return 1
	}
	switch args[0] {
	case "login":
		return cmdLogin(args[1:])
	case "whoami":
		return cmdWhoami(args[1:])
	case "init":
		return cmdInit(args[1:])
	case "gen-dropin":
		return cmdGenDropin(args[1:])
	case "publish":
		return cmdPublish(args[1:])
	case "list":
		return cmdList(args[1:])
	case "rollback":
		return cmdRollback(args[1:])
	case "promote":
		return cmdPromote(args[1:])
	case "version":
		return cmdVersion(args[1:])
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "zen: unknown command %q\n\n", args[0])
		usage()
		return 1
	}
}

func usage() {
	fmt.Print(`zen — OTA updates for Android apps backed by GitHub Releases

Usage:
  zen <command> [flags] [args]

Commands:
  login        authenticate against GitHub and store credentials
  whoami       show the stored credentials and GitHub identity
  init         create zen.config.json for the current project
  gen-dropin   write the ZenUpdater.kt drop-in into the current directory
  publish      publish an APK to a channel (GitHub release + manifest commit)
  list         list published releases per channel
  rollback     make an older release current again
  promote      copy a release from one channel to another
  version      print the zen version
  help         show this help

Run 'zen <command> --help' for the flags of a command.
`)
}

// newFlagSet creates a FlagSet that never exits the process and stays quiet
// on parse errors (the commands print their own usage).
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() {}
	return fs
}

// parseFlags normalizes and parses args. It returns keepGoing to continue
// running the command, or the exit code when --help was requested (0) or
// the flags were invalid (1).
func parseFlags(fs *flag.FlagSet, args []string, summary string) int {
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			helpFor(fs, summary)
			return 0
		}
		fmt.Fprintf(os.Stderr, "zen %s: %v\nRun 'zen %s --help' for usage.\n", fs.Name(), err, fs.Name())
		return 1
	}
	return keepGoing
}

func helpFor(fs *flag.FlagSet, summary string) {
	fmt.Printf("zen %s — %s\n\nUsage: zen %s [flags] [args]\n\nFlags:\n", fs.Name(), summary, fs.Name())
	fs.SetOutput(os.Stdout)
	fs.PrintDefaults()
}

// normalizeArgs re-orders flags before positional arguments so commands
// like `zen rollback 42 --channel beta` parse with the stdlib flag package
// (which stops at the first positional). Values for value-taking flags are
// kept attached; "--" ends reordering.
func normalizeArgs(fs *flag.FlagSet, args []string) []string {
	takesValue := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		bv, ok := f.Value.(interface{ IsBoolFlag() bool })
		if !ok || !bv.IsBoolFlag() {
			takesValue[f.Name] = true
		}
	})
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			out = append(out, args[i:]...)
			return out
		case strings.HasPrefix(a, "--") && len(a) > 2:
			name := a[2:]
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			out = append(out, a)
			if takesValue[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		case len(a) > 1 && a[0] == '-':
			name := a[1:]
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			out = append(out, a)
			if takesValue[name] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
		default:
			out = append(out, a)
		}
	}
	return out
}
