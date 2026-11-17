//go:build !windows

package procmgr

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Alive reports whether pid is a running process (kill -0 semantics).
func Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// ProcessName reads the executable name of a process: /proc/<pid>/exe first
// (handles the " (deleted)" suffix after binary replacement), falling back
// to /proc/<pid>/comm (truncated to 15 chars by the kernel).
func ProcessName(pid int) (string, error) {
	names, err := processNames(pid)
	if err != nil || len(names) == 0 {
		return "", fmt.Errorf("cannot read /proc entry of pid %d", pid)
	}
	return names[0], nil
}

// processNames returns the candidate names of a process: the exe basename
// (e.g. "python3.14") and the kernel comm ("python3"). Version-suffixed exe
// names are why both candidates matter for matching run.command's first word.
func processNames(pid int) ([]string, error) {
	var names []string
	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		names = append(names, strings.TrimSuffix(filepath.Base(exe), " (deleted)"))
	}
	if comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		names = append(names, strings.TrimSpace(string(comm)))
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("cannot read /proc entry of pid %d", pid)
	}
	return names, nil
}

// IsOurs reports whether pid belongs to the expected binary. If neither exe
// nor comm matches (allowing version suffixes like python3.14 for python3),
// the pid is considered someone else's process and must not be killed.
func IsOurs(pid int, expect string) bool {
	if expect == "" {
		return false
	}
	names, err := processNames(pid)
	if err != nil {
		return false
	}
	for _, name := range names {
		if nameMatches(name, expect) {
			return true
		}
	}
	return false
}

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !Alive(pid) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stop terminates the service recorded in workdir/app.pid. Returns whether
// a running process was stopped. A pid whose process is not alive is treated
// as stale; a pid that belongs to a different binary is refused — validated
// against both the expected name (run.command's first word) and the name
// recorded at start time (app.name), which covers wrapper commands like
// `npm start` whose process name differs from the command's first word.
// Services are started with setsid (their own process group), so the whole
// group is signalled to also reach wrapper children.
func Stop(workdir, expect string, logw io.Writer, timeout time.Duration) (bool, error) {
	pidPath := PIDPath(workdir)
	pid, err := ReadPIDFile(pidPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintf(logw, "no pid file at %s: service not running\n", pidPath)
			return false, nil
		}
		fmt.Fprintf(logw, "pid file unreadable (%v), proceeding to start\n", err)
		return false, nil
	}
	if !Alive(pid) {
		fmt.Fprintf(logw, "pid %d in %s is not running (stale), removing\n", pid, pidPath)
		_ = os.Remove(pidPath)
		return false, nil
	}
	recorded := ReadNameFile(workdir)
	if !IsOurs(pid, expect) && !IsOurs(pid, recorded) {
		name, _ := ProcessName(pid)
		return false, fmt.Errorf("pid %d currently runs %q, expected %q (or recorded %q); refusing to kill — fix %s manually",
			pid, name, expect, recorded, pidPath)
	}
	fmt.Fprintf(logw, "stopping service: pid %d (SIGTERM, process group)\n", pid)
	_ = syscall.Kill(-pid, syscall.SIGTERM) // group (setsid: pid is the leader)
	_ = syscall.Kill(pid, syscall.SIGTERM)  // fallback for non-group starts
	if waitGone(pid, timeout) {
		_ = os.Remove(pidPath)
		fmt.Fprintln(logw, "service stopped")
		return true, nil
	}
	fmt.Fprintf(logw, "pid %d still alive after %s, sending SIGKILL\n", pid, timeout)
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if waitGone(pid, 5*time.Second) {
		_ = os.Remove(pidPath)
		return true, nil
	}
	return false, fmt.Errorf("pid %d did not exit after SIGKILL", pid)
}

// Start launches the service in workdir using the setsid+nohup pattern and
// returns the new pid recorded in app.pid. setsid puts the service in its
// own process group so a later Stop can signal wrapper scripts and their
// children together; nohup + the exiting outer sh leave the service
// reparented to init, surviving gitwo restarts without zombies.
func Start(workdir, command string, env []string, logw io.Writer) (int, error) {
	script := fmt.Sprintf("setsid nohup %s >> app.log 2>&1 &\necho $! > app.pid\n", command)
	fmt.Fprintf(logw, "$ sh -c %q (cwd %s)\n", script, workdir)
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = workdir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		fmt.Fprintf(logw, "%s\n", strings.TrimRight(string(out), "\n"))
	}
	if err != nil {
		return 0, fmt.Errorf("start script failed: %w", err)
	}
	var pid int
	for i := 0; i < 20; i++ {
		pid, err = ReadPIDFile(PIDPath(workdir))
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return 0, fmt.Errorf("pid file %s was not written after start: %w", PIDPath(workdir), err)
	}
	return pid, nil
}

// RecordName saves the observed process name to app.name once the process
// has settled (call after the liveness wait — during the exec chain it would
// still read as setsid/nohup). Wrapper commands (npm start, mvn …) exec into
// a process whose name differs from the command's first word; the recorded
// name keeps Stop()'s PID validation reliable for those cases.
func RecordName(workdir string, pid int) {
	if name, err := ProcessName(pid); err == nil {
		_ = os.WriteFile(NamePath(workdir), []byte(name+"\n"), 0o644)
	} else {
		_ = os.Remove(NamePath(workdir))
	}
}

// WaitAlive waits d for the process; returns false as soon as it dies.
func WaitAlive(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if !Alive(pid) {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// CheckPort verifies a TCP port is free before starting a service.
func CheckPort(port int) error {
	if port <= 0 {
		return nil
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("port %d already in use", port)
		}
		return fmt.Errorf("probe port %d: %w", port, err)
	}
	_ = ln.Close()
	return nil
}

// BackupBinary moves the currently installed binary into <dir>/backups and
// prunes old copies, keeping the newest keep files.
func BackupBinary(dst string, keep int, logw io.Writer) error {
	if keep <= 0 {
		return nil
	}
	if _, err := os.Stat(dst); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // nothing installed yet
		}
		return err
	}
	dir := filepath.Join(filepath.Dir(dst), "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102-150405")
	target := filepath.Join(dir, fmt.Sprintf("%s.%s", filepath.Base(dst), stamp))
	if err := os.Rename(dst, target); err != nil {
		return err
	}
	fmt.Fprintf(logw, "backed up %s -> %s\n", dst, target)

	matches, _ := filepath.Glob(filepath.Join(dir, filepath.Base(dst)+".*"))
	sort.Strings(matches) // timestamps sort lexicographically
	if len(matches) > keep {
		for _, m := range matches[:len(matches)-keep] {
			if err := os.Remove(m); err == nil {
				fmt.Fprintf(logw, "pruned old backup %s\n", m)
			}
		}
	}
	return nil
}

// CopyBinary installs src at dst atomically: write to a temp file next to
// dst, then rename. Renaming never hits "text file busy", unlike writing
// onto a running executable.
func CopyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".gitwo-new"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
