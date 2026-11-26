//go:build !linux

package procmgr

import (
	"errors"
	"io"
	"time"
)

// FindListeningPID 在非 Linux 平台不可用（服务托管只支持 Linux）。
func FindListeningPID(port int) (int, error) {
	return 0, errors.New("procmgr: port inspection is only supported on Linux")
}

// ReclaimPort 在非 Linux 平台不可用。
func ReclaimPort(port int, installedBin, programRoot, expectBase string, logw io.Writer, timeout time.Duration) (int, error) {
	return 0, errors.New("procmgr: port reclaim is only supported on Linux")
}
