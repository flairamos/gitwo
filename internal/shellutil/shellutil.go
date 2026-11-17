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
	"syscall"
	"time"
)

// Run executes argv in dir, streaming combined output to logw.
// env == nil inherits the current environment.
func Run(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) error {
	var buf bytes.Buffer
	return runCmd(ctx, dir, env, io.MultiWriter(&buf, logw), io.MultiWriter(&buf, logw), argv)
}

// Output executes argv and returns trimmed stdout; stderr goes to logw.
func Output(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) (string, error) {
	var out bytes.Buffer
	err := runCmd(ctx, dir, env, &out, logw, argv)
	return strings.TrimSpace(out.String()), err
}

// Shell runs a POSIX shell script/command line (sh -c).
func Shell(ctx context.Context, dir string, env []string, logw io.Writer, script string) error {
	return Run(ctx, dir, env, logw, "sh", "-c", script)
}

func runCmd(ctx context.Context, dir string, env []string, stdout, stderr io.Writer, argv []string) error {
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
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(stdout, &tail)
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &tail)
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	var err error
	var killed bool
	select {
	case err = <-waitCh:
	case <-ctx.Done():
		killed = true
		killGroup(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case err = <-waitCh:
		case <-time.After(5 * time.Second):
			killGroup(cmd.Process.Pid, syscall.SIGKILL)
			err = <-waitCh
		}
	}
	if err != nil {
		cmdline := strings.Join(argv, " ")
		var ee *exec.ExitError
		if killed {
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
