//go:build linux

package procmgr

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestFindListeningPID 自己进程监听一个端口，应能通过 /proc 找回自己的 pid。
func TestFindListeningPID(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pid, err := FindListeningPID(port)
	if err != nil {
		t.Fatalf("FindListeningPID(%d): %v", port, err)
	}
	if pid != os.Getpid() {
		t.Fatalf("pid = %d, want %d", pid, os.Getpid())
	}
}

// TestReclaimPortRefusesForeign：占用者不是本仓库二进制时拒绝回收（不误杀）。
func TestReclaimPortRefusesForeign(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pid, err := ReclaimPort(port, filepath.Join(t.TempDir(), "not-our-app"), "", "not-our-app", io.Discard, 200*time.Millisecond)
	if err == nil {
		t.Fatalf("expected refusal for foreign listener")
	}
	if pid != os.Getpid() {
		t.Fatalf("reported pid = %d, want %d", pid, os.Getpid())
	}
	if !Alive(os.Getpid()) {
		t.Fatal("test process was killed unexpectedly")
	}
}

// TestHelperListen 是被 TestReclaimPortKillsMatchingHelper 拉起的子进程：
// 监听指定端口并阻塞，等待父进程回收。
func TestHelperListen(t *testing.T) {
	if os.Getenv("GW_HELPER_LISTEN") != "1" {
		return
	}
	port, _ := strconv.Atoi(os.Getenv("GW_HELPER_PORT"))
	ln, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		os.Exit(3)
	}
	defer ln.Close()
	fmt.Println("READY")
	select {} // 等待被 SIGTERM/SIGKILL
}

// TestReclaimPortKillsMatchingHelper：占用者就是“本程序二进制”时回收成功。
func TestReclaimPortKillsMatchingHelper(t *testing.T) {
	lib, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lib.Addr().(*net.TCPAddr).Port
	lib.Close() // 交给子进程监听

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^TestHelperListen$")
	cmd.Env = append(os.Environ(), "GW_HELPER_LISTEN=1", "GW_HELPER_PORT="+strconv.Itoa(port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line == "" {
		t.Fatalf("helper did not signal ready: %q, %v", line, err)
	}

	pid, err := FindListeningPID(port)
	if err != nil {
		t.Fatalf("FindListeningPID: %v", err)
	}
	// 子进程 exe 就是测试二进制：用 self 作为 installedBin 应命中
	killed, err := ReclaimPort(port, self, "", filepath.Base(self), io.Discard, 2*time.Second)
	if err != nil {
		t.Fatalf("ReclaimPort: %v", err)
	}
	if killed != pid {
		t.Fatalf("reclaimed pid = %d, want %d", killed, pid)
	}
	// 收割子进程后再断言（等待期间可能是僵尸态）；被信号终止是预期结果
	if err := procmgrWait(cmd); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("helper wait: %v", err)
		}
	}
	if Alive(pid) {
		t.Fatalf("pid %d still alive after reclaim", pid)
	}
}

func procmgrWait(cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		return fmt.Errorf("helper did not exit")
	}
}
