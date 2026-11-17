package shellutil

import (
	"strings"
)

// tailBuffer 只保留最后 max 字节输出：命令输出可以很大（构建日志成百 MB），
// 若在内存里无限累积会把服务器内存吃满；这里用定长缓冲保证 O(max) 内存。
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		copy(t.buf, t.buf[over:])
		t.buf = t.buf[:t.max]
	}
	return n, nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// lastOutput returns the tail of a command's combined output, trimmed to at
// most max bytes at a line boundary. Platform-neutral so it is testable on
// every OS gitwo builds on.
func lastOutput(out string, max int) string {
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
