//go:build windows

// Windows stub: gitwo targets Linux servers (Ubuntu / CentOS Stream).
// Building on Windows is only for development convenience; runtime behavior
// that depends on POSIX process groups is not supported.
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
	"time"
)

var errUnsupported = errors.New("shellutil: POSIX process management is not supported on windows; run gitwo on Linux")

// Run executes argv in dir, streaming combined output to logw.
func Run(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	if logw == nil {
		logw = io.Discard
	}
	cmd.Stdout = logw
	cmd.Stderr = logw
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	select {
	case <-ctx.Done():
		return fmt.Errorf("%s: killed (%v)", strings.Join(argv, " "), ctx.Err())
	default:
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Errorf("%s: exited with %s", strings.Join(argv, " "), ee)
		}
	}
	return err
}

// Output executes argv and returns trimmed stdout; stderr goes to logw.
func Output(ctx context.Context, dir string, env []string, logw io.Writer, argv ...string) (string, error) {
	var out bytes.Buffer
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	cmd.Stdout = &out
	if logw != nil {
		cmd.Stderr = logw
	}
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

// Shell is unsupported on windows.
func Shell(ctx context.Context, dir string, env []string, logw io.Writer, script string) error {
	return errUnsupported
}

// ShellStall is unsupported on windows.
func ShellStall(ctx context.Context, dir string, env []string, logw io.Writer, stall time.Duration, script string) error {
	return errUnsupported
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
