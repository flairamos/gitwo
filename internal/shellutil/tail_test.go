package shellutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestLastOutput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var b bytes.Buffer
		if got := lastOutput(&b, 400); got != "" {
			t.Errorf("empty buffer: %q", got)
		}
	})
	t.Run("short", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("sh: 1: npm: not found\n")
		got := lastOutput(&b, 400)
		if !strings.Contains(got, "npm: not found") || !strings.HasPrefix(got, "  | ") {
			t.Errorf("short tail: %q", got)
		}
	})
	t.Run("long-truncates-at-line-boundary", func(t *testing.T) {
		var b bytes.Buffer
		for i := 0; i < 100; i++ {
			b.WriteString("line-of-output-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX\n")
		}
		got := lastOutput(&b, 80)
		if len(got) > 400 {
			t.Errorf("tail too long: %d", len(got))
		}
		if strings.Contains(got, "line-of-output-0\n") {
			t.Error("old lines should be dropped")
		}
		if !strings.Contains(got, "  | line-of-output-") {
			t.Errorf("expected prefixed lines: %q", got)
		}
	})
	t.Run("whitespace-only", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("\n\n\n")
		if got := lastOutput(&b, 400); got != "" {
			t.Errorf("whitespace-only: %q", got)
		}
	})
}
