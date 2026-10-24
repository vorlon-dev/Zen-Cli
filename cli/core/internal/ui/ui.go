// Package ui provides the small terminal helpers the Zen CLI uses: ANSI
// colors (with NO_COLOR + non-TTY detection), a step printer, a table
// printer and interactive prompts. Everything is standard library only.
package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

var colorEnabled = detectColor()

func detectColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Color reports whether ANSI colors will be emitted on stdout.
func Color() bool { return colorEnabled }

func wrap(code, s string) string {
	if !colorEnabled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Green, Yellow, Red, Bold and Dim colorize a string when colors are on.
func Green(s string) string  { return wrap("32", s) }
func Yellow(s string) string { return wrap("33", s) }
func Red(s string) string    { return wrap("31", s) }
func Bold(s string) string   { return wrap("1", s) }
func Dim(s string) string    { return wrap("2", s) }

var stdin = bufio.NewReader(os.Stdin)

// PrintStep runs fn while printing "▸ label … done (1.2s)" — or "failed" —
// on a single line. It returns fn's error for convenient composition:
//
//	if err := ui.PrintStep("parsing APK", func() error { ... }); err != nil { ... }
func PrintStep(label string, fn func() error) error {
	fmt.Printf("▸ %s … ", label)
	start := time.Now()
	err := fn()
	d := elapsed(start)
	if err != nil {
		fmt.Printf("%s (%s)\n", Red("failed"), d)
		return err
	}
	fmt.Printf("%s (%s)\n", Green("done"), d)
	return nil
}

func elapsed(start time.Time) string {
	d := time.Since(start)
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// Table renders simple left-aligned padded tables.
type Table struct {
	headers []string
	rows    [][]string
}

// NewTable creates a table with the given header cells.
func NewTable(headers ...string) *Table { return &Table{headers: headers} }

// AddRow appends one row of cells.
func (t *Table) AddRow(cells ...string) { t.rows = append(t.rows, cells) }

func (t *Table) widths() []int {
	w := make([]int, len(t.headers))
	for i, h := range t.headers {
		w[i] = len([]rune(h))
	}
	for _, r := range t.rows {
		for i, c := range r {
			if i < len(w) && len([]rune(c)) > w[i] {
				w[i] = len([]rune(c))
			}
		}
	}
	return w
}

// String renders the table (header in bold, rule line, padded rows).
func (t *Table) String() string {
	w := t.widths()
	var b strings.Builder
	line := func(cells []string, colorize func(string) string) {
		for i, c := range cells {
			if i >= len(w) {
				continue
			}
			pad := w[i] - len([]rune(c))
			if pad < 0 {
				pad = 0
			}
			b.WriteString(colorize(c))
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString("  ")
		}
		b.WriteString("\n")
	}
	line(t.headers, Bold)
	for _, wd := range w {
		b.WriteString(strings.Repeat("─", wd))
		b.WriteString("  ")
	}
	b.WriteString("\n")
	for _, r := range t.rows {
		line(r, func(s string) string { return s })
	}
	return b.String()
}

// Print renders the table to stdout.
func (t *Table) Print() { fmt.Print(t.String()) }

// Confirm asks a yes/no question on stdin. Anything except y/yes is "no".
// EOF counts as "no".
func Confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, err := stdin.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// Prompt reads a line from stdin, returning def when the user presses enter
// (the default is shown in brackets).
func Prompt(label, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := stdin.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return def
	}
	v := strings.TrimSpace(line)
	if v == "" {
		return def
	}
	return v
}

// Success prints a green check line to stdout.
func Success(format string, a ...any) { fmt.Println(Green("✔ " + fmt.Sprintf(format, a...))) }

// Warn prints a yellow warning line to stderr.
func Warn(format string, a ...any) { fmt.Fprintln(os.Stderr, Yellow("⚠ "+fmt.Sprintf(format, a...))) }

// Err prints a red error line to stderr.
func Err(format string, a ...any) { fmt.Fprintln(os.Stderr, Red("✖ "+fmt.Sprintf(format, a...))) }

// Detail prints a dim informational line to stdout.
func Detail(format string, a ...any) { fmt.Println(Dim(fmt.Sprintf(format, a...))) }
