// Package procmgr manages backend services started with the nohup pattern:
// `nohup <command> >> app.log 2>&1 & echo $! > app.pid` inside the service
// workdir. It covers stopping via pid file (with PID-reuse validation),
// liveness checks, port probes, binary install and backup rotation.
package procmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PIDFileName is the pid file kept inside the service workdir.
const PIDFileName = "app.pid"

// NameFileName records the actual process name observed right after start.
// Wrapper-style start commands (npm start, mvn spring-boot:run, …) exec into
// a process whose name differs from the command's first word; the recorded
// name keeps PID validation reliable for those cases too.
const NameFileName = "app.name"

// PIDPath returns the pid file location inside workdir.
func PIDPath(workdir string) string { return filepath.Join(workdir, PIDFileName) }

// NamePath returns the recorded process-name file location inside workdir.
func NamePath(workdir string) string { return filepath.Join(workdir, NameFileName) }

// ReadNameFile returns the recorded process name, or "" when absent.
func ReadNameFile(workdir string) string {
	b, err := os.ReadFile(NamePath(workdir))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// nameMatches compares an observed process name against the expected one:
// exact match, kernel-truncated comm (15 chars), or version-suffixed exe
// (python3.14 matches python3).
func nameMatches(name, expect string) bool {
	if name == expect {
		return true
	}
	if len(expect) > 15 && strings.HasPrefix(expect, name) {
		return true
	}
	if strings.HasPrefix(name, expect) && len(expect) >= 3 {
		if tail := name[len(expect):]; isVersionTail(tail) {
			return true
		}
	}
	return false
}

func isVersionTail(s string) bool {
	if s == "" || (s[0] != '.' && (s[0] < '0' || s[0] > '9')) {
		return false
	}
	for _, c := range s {
		if c != '.' && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// QuoteSh single-quotes a string for safe inclusion in a POSIX shell script.
func QuoteSh(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// ExpectBinary derives the process/binary name a command should show up as
// in /proc (e.g. "./bin/app --port 8080" -> "app"). It is used to validate
// that a pid still belongs to our service before killing it.
func ExpectBinary(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// ReadPIDFile parses a pid file.
func ReadPIDFile(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(b))
	pid, err := strconv.Atoi(s)
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid pid file %s: %q", path, s)
	}
	return pid, nil
}
