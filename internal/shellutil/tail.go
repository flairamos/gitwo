package shellutil

import (
	"bytes"
	"strings"
)

// lastOutput returns the tail of a command's combined output, trimmed to at
// most max bytes at a line boundary. Platform-neutral so it is testable on
// every OS gitwo builds on.
func lastOutput(buf *bytes.Buffer, max int) string {
	out := buf.String()
	out = strings.TrimRight(out, "\n")
	if out == "" {
		return ""
	}
	if len(out) > max {
		out = out[len(out)-max:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = "  | " + lines[i]
	}
	return strings.Join(lines, "\n")
}
