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
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Run executes argv in dir, streaming combined output to logw.
// env == nil inherits the current environment.
func Run(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) error {
	if logw == nil {
		logw = io.Discard
	}
	return runCmd(ctx, dir, env, logw, logw, 0, argv)
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

// ShellStall 同 Shell，但增加“停滞检测”：命令连续 stall 时间没有任何输出、
// 且进程组几乎不消耗 CPU（典型场景：依赖下载卡死/网络不可达）时，终止整个
// 进程组并返回停滞超时错误，让流水线快速失败而不是等满总超时。“有输出”
// 或“CPU 忙”（go build 编译期是静默的）都不算停滞。stall <= 0 时等同 Shell。
func ShellStall(ctx context.Context, dir string, env []string, logw io.Writer, stall time.Duration, script string) error {
	if logw == nil {
		logw = io.Discard
	}
	return runCmd(ctx, dir, env, logw, logw, stall, []string{"sh", "-c", script})
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
	tail := &tailBuffer{max: 16 << 10} // 只留最后 16 KiB 供失败诊断，内存不随输出增长
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	idle := func(w io.Writer) io.Writer {
		if stall <= 0 {
			return w
		}
		return &idleWriter{w: w, last: &lastOut}
	}
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(idle(stdout), tail)
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(idle(stderr), tail)
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// 停滞检测 watchdog：连续 stall 时间无任何输出且进程组近乎零 CPU → 判定
	// 卡死。go build/npm 编译期可能长时间静默但 CPU 忙，纯“无输出”判定会误杀。
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
			// CPU 回看窗口：判定前要求最近 quiet 时间内进程组几乎没消耗 CPU。
			// 超过 5s 的 stall 也只回看 5s——真正的卡死（网络等待）CPU 接近 0。
			quiet := stall
			if quiet > 5*time.Second {
				quiet = 5 * time.Second
			}
			type sample struct {
				at  time.Time
				cpu uint64
			}
			var hist []sample
			procOK := true // /proc 不可用（非 Linux unix）时退回纯无输出判定
			t := time.NewTicker(iv)
			defer t.Stop()
			for {
				select {
				case <-done:
					return
				case now := <-t.C:
					if time.Since(time.Unix(0, lastOut.Load())) < stall {
						hist = hist[:0] // 输出恢复：静默期重新计时
						continue
					}
					if !procOK {
						close(stallCh)
						return
					}
					cpu, found := procGroupCpuTicks(cmd.Process.Pid)
					if !found {
						// 进程组已不存在：等 waitCh 收尾，不强行判定
						return
					}
					hist = append(hist, sample{now, cpu})
					for len(hist) > 0 && now.Sub(hist[0].at) > quiet+iv {
						hist = hist[1:]
					}
					if len(hist) < 2 {
						continue
					}
					delta := hist[len(hist)-1].cpu - hist[0].cpu
					span := hist[len(hist)-1].at.Sub(hist[0].at)
					if span < quiet {
						continue
					}
					// /proc stat 的 CPU 以 clock ticks 计，USER_HZ 固定为 100
					// （proc(5)）。阈值取窗口墙钟时间的 5%：编译/打包远高于此，
					// 卡死的下载与网络等待接近 0。
					if delta <= uint64(span.Seconds()*100*0.05) {
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
				return fmt.Errorf("%s: 停滞超过 %v（无输出且几乎无 CPU 活动，常见于依赖下载卡死或网络不可达），已终止", cmdline, stall)
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s: 执行超时，进程组已终止——请检查 data/server.yaml 的 timeouts 配置，"+
					"或确认命令是否卡在依赖下载/编译（可调大超时或预热依赖缓存）", cmdline)
			}
			return fmt.Errorf("%s: killed (%v)", cmdline, ctx.Err())
		}
		if errors.As(err, &ee) {
			msg := fmt.Sprintf("%s: exited with %s", cmdline, ee)
			if out := lastOutput(tail.String(), 400); out != "" {
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

// procGroupCpuTicks 汇总进程组 pgid 内所有进程的 utime+stime（clock ticks），
// 通过扫描 /proc 实现（子进程存活期间其 CPU 只记在自己的 stat 里，父进程的
// cutime 只含已回收的子进程）。ok=false 表示 /proc 不可用或进程组已消失，
// 调用方据此退回纯无输出判定或放弃本轮判定。
func procGroupCpuTicks(pgid int) (uint64, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	var total uint64
	found := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue // 进程刚好退出：跳过
		}
		// stat 形如 "pid (comm) state ppid pgrp …"；comm 可含空格与括号，
		// 以最后一个 ')' 切开后再按空白分列。切出的字段：[0]=state、
		// [2]=pgrp、[11]=utime、[12]=stime。
		i := strings.LastIndexByte(string(data), ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(string(data[i+1:]))
		if len(fields) < 13 {
			continue
		}
		pgrp, err := strconv.Atoi(fields[2])
		if err != nil || pgrp != pgid {
			continue
		}
		found = true
		ut, _ := strconv.ParseUint(fields[11], 10, 64)
		st, _ := strconv.ParseUint(fields[12], 10, 64)
		total += ut + st
	}
	return total, found
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
