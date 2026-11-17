package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.log")

	if got := TailFile(filepath.Join(dir, "missing"), 100); got != "" {
		t.Errorf("missing file: %q", got)
	}

	os.WriteFile(p, []byte(""), 0o644)
	if got := TailFile(p, 100); got != "" {
		t.Errorf("empty file: %q", got)
	}

	content := strings.Repeat("line\n", 100) // 500 bytes
	os.WriteFile(p, []byte(content), 0o644)

	if got := TailFile(p, 1000); got != content {
		t.Error("small max < size should return whole file")
	}
	tail := TailFile(p, 200)
	if len(tail) > 200 || strings.HasPrefix(tail, "line") && !strings.HasPrefix(tail, "line\n") {
		t.Errorf("tail should drop torn first line: %q", tail)
	}
	if !strings.HasSuffix(tail, "line\n") && tail != "" {
		t.Errorf("tail should end at file end: %q", tail)
	}
}
