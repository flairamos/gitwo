package shellutil

import (
	"strings"
	"sync"
)

// tailBuffer 只保留最后 max 字节输出：命令输出可以很大（构建日志成百 MB），
// 若在内存里无限累积会把服务器内存吃满；这里用定长缓冲保证 O(max) 内存。
// 子命令的 stdout 与 stderr 由 exec 的两个拷贝 goroutine 并发写入同一个
// tailBuffer，所以必须加锁（-race 下曾报出这里的数据竞争）。
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	t.mu.Lock()
	defer t.mu.Unlock()
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

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

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
