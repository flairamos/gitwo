//go:build windows

// Windows stub: gitwo targets Linux servers. See shellutil_windows.go.
package procmgr

import (
	"errors"
	"io"
	"time"
)

var errUnsupported = errors.New("procmgr: POSIX process management is not supported on windows; run gitwo on Linux")

func Alive(pid int) bool                 { return false }
func IsOurs(pid int, expect string) bool { return false }

func ProcessName(pid int) (string, error) { return "", errUnsupported }

func Stop(workdir, expect string, logw io.Writer, timeout time.Duration) (bool, error) {
	return false, errUnsupported
}

func Start(workdir, command string, env []string, logw io.Writer) (int, error) {
	return 0, errUnsupported
}

func WaitAlive(pid int, d time.Duration) bool { return false }

func RecordName(workdir string, pid int) {}

func CheckPort(port int) error { return errUnsupported }

func BackupBinary(dst string, keep int, logw io.Writer) error { return errUnsupported }

func CopyBinary(src, dst string) error { return errUnsupported }
