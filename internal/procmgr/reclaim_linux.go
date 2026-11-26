//go:build linux

package procmgr

// 端口占用回收：部署重启服务前如果发现端口被占用，可能是上次手动
// `nohup ./app &` 启动、没有被 app.pid 跟踪的旧实例（用户自己启动的，
// Stop 无从感知）。这里通过 /proc 找出监听该端口的进程，只在确认它运行
// 的是“本仓库安装的二进制 / 程序目录下的同名二进制”时才回收，避免误杀
// 其它服务；其它进程一律拒绝并报出 pid/命令行，由用户手动处理。

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// FindListeningPID 返回监听 TCP port 的进程 pid（IPv4/IPv6 均可）。
func FindListeningPID(port int) (int, error) {
	inodes, err := listeningInodes(port)
	if err != nil {
		return 0, err
	}
	if len(inodes) == 0 {
		return 0, fmt.Errorf("port %d: no TCP listener found", port)
	}
	procs, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("scan /proc: %w", err)
	}
	for _, p := range procs {
		pid, err := strconv.Atoi(p.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir("/proc/" + p.Name() + "/fd")
		if err != nil {
			continue // 权限不足等：跳过
		}
		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + p.Name() + "/fd/" + fd.Name())
			if err != nil {
				continue
			}
			inode, ok := socketInode(link)
			if ok && inodes[inode] {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("port %d: listener pid not found (need permission to inspect /proc)", port)
}

// listeningInodes 解析 /proc/net/tcp{,6}，返回该端口 LISTEN 状态的 socket inode 集合。
func listeningInodes(port int) (map[string]bool, error) {
	want := fmt.Sprintf("%04X", port)
	out := map[string]bool{}
	for _, name := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(name)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		first := true
		for sc.Scan() {
			if first {
				first = false
				continue
			}
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 {
				continue
			}
			// fields: sl local_address rem_address st ... inode(9)
			local, st, inode := fields[1], fields[3], fields[9]
			if st != "0A" { // TCP_LISTEN
				continue
			}
			if i := strings.LastIndexByte(local, ':'); i > 0 && strings.EqualFold(local[i+1:], want) {
				out[inode] = true
			}
		}
		_ = f.Close()
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("port %d: not in LISTEN state", port)
	}
	return out, nil
}

func socketInode(link string) (string, bool) {
	const p = "socket:["
	if strings.HasPrefix(link, p) && strings.HasSuffix(link, "]") {
		return link[len(p) : len(link)-1], true
	}
	return "", false
}

// ReclaimPort 尝试回收监听 port 的“旧实例”。仅当该进程的可执行文件是
// installedBin（部署安装的二进制），或是 programRoot 目录下、文件名等于
// expectBase 的二进制时，才发送 SIGTERM→SIGKILL。返回被回收的 pid；
// 确认不是本仓库的进程时返回错误（不杀）。
func ReclaimPort(port int, installedBin, programRoot, expectBase string, logw io.Writer, timeout time.Duration) (int, error) {
	pid, err := FindListeningPID(port)
	if err != nil {
		return 0, err
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return pid, fmt.Errorf("port %d held by pid %d, read exe: %w", port, pid, err)
	}
	exe = strings.TrimSuffix(exe, " (deleted)") // 二进制已被新版本替换（rename）时
	if !sameBinary(exe, installedBin, programRoot, expectBase) {
		cmd := strings.TrimSpace(readCmdline(pid))
		return pid, fmt.Errorf("port %d is held by pid %d (%s), not this repo's binary; refusing to kill — stop it manually", port, pid, cmd)
	}
	fmt.Fprintf(logw, "port %d is held by stale instance pid %d (%s), started outside gitwo; reclaiming\n", port, pid, exe)
	if pgrp, gerr := syscall.Getpgid(pid); gerr == nil && pgrp == pid {
		_ = syscall.Kill(-pid, syscall.SIGTERM) // 自己是进程组组长：连同子进程
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if goneOrZombie(pid, timeout) {
		return pid, nil
	}
	fmt.Fprintf(logw, "pid %d still alive after %s, sending SIGKILL\n", pid, timeout)
	if pgrp, gerr := syscall.Getpgid(pid); gerr == nil && pgrp == pid {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if goneOrZombie(pid, 5*time.Second) {
		return pid, nil
	}
	return pid, fmt.Errorf("pid %d did not exit after SIGKILL", pid)
}

// goneOrZombie 判断进程是否已退出；僵尸进程（已退出但父进程尚未 Wait）也算
// 退出——否则作为父进程的调用方会一直等不到“消失”。
func goneOrZombie(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !Alive(pid) || isZombie(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func isZombie(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(b)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] == 'Z'
	}
	return false
}

// sameBinary 判断 exe 是否属于“我们的”服务二进制。
func sameBinary(exe, installedBin, programRoot, expectBase string) bool {
	if installedBin != "" {
		if abs, err := filepath.Abs(exe); err == nil && abs == filepath.Clean(installedBin) {
			return true
		}
	}
	if programRoot == "" || expectBase == "" {
		return false
	}
	absExe, err := filepath.Abs(exe)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(programRoot)
	if err != nil {
		return false
	}
	if filepath.Base(absExe) != expectBase {
		return false
	}
	return absExe == absRoot || strings.HasPrefix(absExe, absRoot+string(os.PathSeparator))
}

func readCmdline(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " ")
}
