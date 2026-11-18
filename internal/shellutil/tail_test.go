package shellutil

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestLastOutput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		var b bytes.Buffer
		if got := lastOutput(b.String(), 400); got != "" {
			t.Errorf("empty buffer: %q", got)
		}
	})
	t.Run("short", func(t *testing.T) {
		var b bytes.Buffer
		b.WriteString("sh: 1: npm: not found\n")
		got := lastOutput(b.String(), 400)
		if !strings.Contains(got, "npm: not found") || !strings.HasPrefix(got, "  | ") {
			t.Errorf("short tail: %q", got)
		}
	})
	t.Run("long-truncates-at-line-boundary", func(t *testing.T) {
		var b bytes.Buffer
		for i := 0; i < 100; i++ {
			b.WriteString("line-of-output-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX\n")
		}
		got := lastOutput(b.String(), 80)
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
		if got := lastOutput(b.String(), 400); got != "" {
			t.Errorf("whitespace-only: %q", got)
		}
	})
}

// TestTailBufferBounded：大输出只保留最后 max 字节，内存不随输出增长。
func TestTailBufferBounded(t *testing.T) {
	tb := &tailBuffer{max: 64}
	chunk := strings.Repeat("x", 1024)
	for i := 0; i < 1024; i++ { // 写入 1 MiB
		if _, err := tb.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(tb.String()); got != 64 {
		t.Fatalf("tail len = %d, want 64", got)
	}
	// 大块写入（超过 max）取的是尾部
	tb2 := &tailBuffer{max: 8}
	tb2.Write([]byte("0123456789abcdef"))
	if tb2.String() != "89abcdef" {
		t.Errorf("tail = %q", tb2.String())
	}
	// 跨多次写入时内容保持为最后 N 字节
	tb3 := &tailBuffer{max: 5}
	tb3.Write([]byte("abc"))
	tb3.Write([]byte("defgh"))
	if tb3.String() != "defgh" {
		t.Errorf("tail = %q", tb3.String())
	}
}

// TestTailBufferConcurrentWrites：stdout/stderr 两个拷贝 goroutine 会并发写同
// 一个 tailBuffer，必须无数据竞争（用 -race 跑本测试即校验），且缓存仍有界。
func TestTailBufferConcurrentWrites(t *testing.T) {
	tb := &tailBuffer{max: 1024}
	chunk := []byte(strings.Repeat("y", 64))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				if _, err := tb.Write(chunk); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := len(tb.String()); got > 1024 {
		t.Fatalf("tail len = %d, want <= 1024", got)
	}
}
