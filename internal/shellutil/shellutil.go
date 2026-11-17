//go:build !windows

// Package shellutil runs external commands with gitwo's execution rules:
// combined output streamed to a log writer, the child in its own process
// group so timeouts kill the whole tree, and deadline handling.
package shellutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Run executes argv in dir, streaming combined output to logw.
// env == nil inherits the current environment.
func Run(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) error {
	var buf bytes.Buffer
	return runCmd(ctx, dir, env, io.MultiWriter(&buf, logw), io.MultiWriter(&buf, logw), 0, argv)
}

// Output executes argv and returns trimmed stdout; stderr goes to logw.
func Output(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) (string, error) {
	var out bytes.Buffer
	err := runCmd(ctx, dir, env, &out, logw, 0, argv)
	return strings.TrimSpace(out.String()), err
}

// Shell runs a POSIX shell script/command line (sh -c).
func Shell(ctx context.Context, dir string, env []string, logw io.Writer, script string) error {
	return Run(ctx, dir, env, logw, "sh", "-c", script)
}

// ShellStall 同 Shell，但增加“停滞检测”：命令连续 stall 时间没有任何输出
// （典型场景：依赖下载卡死/网络不可达）时，终止整个进程组并返回停滞超时
// 错误，让流水线快速失败而不是等满总超时。stall <= 0 时等同 Shell。
func ShellStall(ctx context.Context, dir string, env []string, logw io.Writer, stall time.Duration, script string) error {
	var buf bytes.Buffer
	return runCmd(ctx, dir, env, io.MultiWriter(&buf, logw), io.MultiWriter(&buf, logw), stall, []string{"sh", "-c", script})
}

// idleWriter 包装输出 writer，记录最后一次写入时间供停滞检测使用。
type idleWriter struct {
	w    io.Writer
	last *atomic.Int64
}

func (i *idleWriter) Write(p []byte) (int, error) {
	i.last.Store(time.Now().UnixNano())
	return i.w.Write(p)
}

func runCmd(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, stall time.Duration, argv []string) error {
	if len(argv) == 0 {
		return errors.New("shellutil: empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	// Own process group: lets us kill the whole subtree (npm spawns children).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 10 * time.Second
	// Keep a tail of the output so failures can surface the actual reason
	// (e.g. "npm: not found" when a toolchain is missing).
	var tail bytes.Buffer
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	idle := func(w io.Writer) io.Writer {
		if stall <= 0 {
			return w
		}
		return &idleWriter{w: w, last: &lastOut}
	}
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(idle(stdout), &tail)
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(idle(stderr), &tail)
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// 停滞检测 watchdog：连续 stall 时间无任何输出 → 判定卡死
	stallCh := make(chan struct{})
	done := make(chan struct{})
	defer close(done)
	if stall > 0 {
		go func() {
			iv := stall / 4
			if iv < 50*time.Millisecond {
				iv = 50 * time.Millisecond
			}
			if iv > time.Second {
				iv = time.Second
			}
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case <-t.C:
					if time.Since(time.Unix(0, lastOut.Load())) >= stall {
						close(stallCh)
						return
					}
				}
			}
		}()
	}

	var err error
	var killed, stalled bool
	select {
	case err = <-waitCh:
	case <-ctx.Done():
		killed = true
		err = killAndWait(cmd, waitCh)
	case <-stallCh:
		killed, stalled = true, true
		err = killAndWait(cmd, waitCh)
	}
	if err != nil {
		cmdline := strings.Join(argv, " ")
		var ee *exec.ExitError
		if killed {
			if stalled {
				return fmt.Errorf("%s: 停滞超过 %v（无任何输出，常见于依赖下载卡死或网络不可达），已终止", cmdline, stall)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s: 执行超时，进程组已终止——请检查 data/server.yaml 的 timeouts 配置，"+
					"或确认命令是否卡在依赖下载/编译（可调大超时或预热依赖缓存）", cmdline)
			}
			return fmt.Errorf("%s: killed (%v)", cmdline, ctx.Err())
		}
		if errors.As(err, &ee) {
			msg := fmt.Sprintf("%s: exited with %s", cmdline, ee)
			if out := lastOutput(&tail, 400); out != "" {
				// Surface the actual reason (e.g. "npm: not found") so missing
				// toolchains produce an understandable pipeline failure.
				msg += "\n" + out
			}
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// killAndWait 向进程组发 SIGTERM，5 秒未退再 SIGKILL，返回最终等待结果。
func killAndWait(cmd *exec.Cmd, waitCh <-chan error) error {
	killGroup(cmd.Process.Pid, syscall.SIGTERM)
	select {
	case err := <-waitCh:
		return err
	case <-time.After(5 * time.Second):
		killGroup(cmd.Process.Pid, syscall.SIGKILL)
		return <-waitCh
	}
}

// killGroup signals the process group led by pid (Setpgid makes the child
// its own group leader). pid <= 1 is guarded to never signal init.
func killGroup(pid int, sig syscall.Signal) {
	if pid > 1 {
		_ = syscall.Kill(-pid, sig)
	}
}

// EnvLayer builds a sorted environment slice: current environment overlaid
// with the given maps (later maps win).
func EnvLayer(extra ...map[string]string) []string {
	m := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	for _, layer := range extra {
		for k, v := range layer {
			m[k] = v
		}
	}
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
