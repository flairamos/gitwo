// Package fsutil holds small filesystem helpers shared across packages.
package fsutil

import (
	"os"
	"strings"
)

// TailFile returns the last max bytes of a file, dropping a possibly torn
// first line. Returns "" when the file is missing or unreadable.
func TailFile(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	size := st.Size()
	var off int64
	if size > max {
		off = size - max
	}
	buf := make([]byte, size-off)
	if _, err := f.ReadAt(buf, off); err != nil && len(buf) == 0 {
		return ""
	}
	s := string(buf)
	if off > 0 {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return s
}
