// Package output provides one rendering path shared by every command: build a
// plain data shape first, then either JSON-encode it or print it as a table.
// This avoids duplicating render logic per command (a wart in the jobtracker
// reference tool, which had near-copy-paste plain/rich renderers per command).
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"golang.org/x/term"
)

// Table is the plain shape every command builds before rendering.
type Table struct {
	Headers []string
	Rows    [][]string
}

// IsTTY reports whether stdout is an interactive terminal. Color/table
// formatting is only ever applied when this is true; scripted/agent callers
// (and anyone piping output) get plain, stable text.
func IsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Render writes v as either indented JSON (when asJSON is true) or as a table
// derived from toTable(v). Every command constructs its own typed result
// struct and a small toTable closure; this function is the only place either
// format actually gets written.
func Render(w io.Writer, asJSON bool, v any, toTable func() Table) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	return RenderTable(w, toTable())
}

// RenderTable prints t as a whitespace-aligned table using tabwriter.
func RenderTable(w io.Writer, t Table) error {
	if len(t.Rows) == 0 {
		_, err := fmt.Fprintln(w, "(no rows)")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	if len(t.Headers) > 0 {
		fmt.Fprintln(tw, joinTab(t.Headers))
	}
	for _, row := range t.Rows {
		fmt.Fprintln(tw, joinTab(row))
	}
	return tw.Flush()
}

func joinTab(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += "\t"
		}
		out += f
	}
	return out
}
