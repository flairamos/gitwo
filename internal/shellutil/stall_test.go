//go:build !windows

package shellutil

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestShellStallKillsSilentCommand：连续没有任何输出超过 stall 阈值时，
// 终止进程组并返回“停滞”错误（典型场景：依赖下载卡死）。
func TestShellStallKillsSilentCommand(t *testing.T) {
	var log bytes.Buffer
	start := time.Now()
	err := ShellStall(context.Background(), "", nil, &log, 300*time.Millisecond, "sleep 5; echo done")
	if err == nil {
		t.Fatal("expected stall error")
	}
	if !strings.Contains(err.Error(), "停滞超过") {
		t.Errorf("err = %v, want 停滞超过", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("took %v, want quick kill", took)
	}
}

// TestShellStallProgressingCommand：命令持续有输出时不受停滞检测影响。
func TestShellStallProgressingCommand(t *testing.T) {
	var log bytes.Buffer
	err := ShellStall(context.Background(), "", nil, &log, 500*time.Millisecond,
		"i=0; while [ $i -lt 4 ]; do echo step-$i; i=$((i+1)); sleep 0.3; done")
	if err != nil {
		t.Fatalf("err = %v, log=%s", err, log.String())
	}
	if !strings.Contains(log.String(), "step-3") {
		t.Errorf("log missing progress output: %q", log.String())
	}
}

// TestShellStallDisabled：stall=0 关闭检测，静默命令正常完成（不回归）。
func TestShellStallDisabled(t *testing.T) {
	var log bytes.Buffer
	if err := ShellStall(context.Background(), "", nil, &log, 0, "sleep 0.5; echo ok"); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(log.String(), "ok") {
		t.Errorf("log = %q", log.String())
	}
}
