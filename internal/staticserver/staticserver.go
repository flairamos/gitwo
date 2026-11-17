// Package staticserver is gitwo's built-in replacement for Nginx in
// "dual" mode: a minimal net/http file server with SPA fallback. It runs as
// a separate long-lived process (`gitwo serve-static`), spawned once by
// gitwo and kept resident — content updates are applied by rsync alone.
package staticserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gitwo/internal/fsutil"
	"gitwo/internal/procmgr"
)

// Handler serves files from dir; when spa is on, requests that do not match
// any file fall back to dir/index.html (client-side routing support).
func Handler(dir string, spa bool) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if spa {
			upath := path.Clean("/" + r.URL.Path)
			full := filepath.Join(dir, filepath.FromSlash(upath))
			if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
				idx := filepath.Join(dir, "index.html")
				if _, err2 := os.Stat(idx); err2 == nil {
					w.Header().Set("Cache-Control", "no-cache")
					http.ServeFile(w, r, idx)
					return
				}
			}
		}
		files.ServeHTTP(w, r)
	})
}

// Serve runs the static server until SIGINT/SIGTERM. Used by the
// `gitwo serve-static` subcommand.
func Serve(addr, dir string, spa bool) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           Handler(dir, spa),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	fmt.Printf("static server listening on %s, serving %s (spa=%t)\n", addr, dir, spa)
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	}
}

// StaticSpec describes one managed static server instance.
type StaticSpec struct {
	Addr    string // listen addr, e.g. 0.0.0.0:3000
	Dir     string // static dir to serve
	SPA     bool
	Slug    string // repo slug, used for pid/log file names
	PIDDir  string // state dir holding <slug>-static.pid
	LogPath string // long-lived log file of the static server
	SelfExe string // path to the gitwo binary
}

// EnsureRunning starts the static server for this spec if it is not already
// running. Content changes need no restart: rsync updates the directory.
func EnsureRunning(sp StaticSpec, logw io.Writer) error {
	if err := os.MkdirAll(sp.PIDDir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(sp.Dir, 0o755); err != nil {
		return fmt.Errorf("create static dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(sp.LogPath), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	pidPath := filepath.Join(sp.PIDDir, sp.Slug+"-static.pid")

	if pid, err := procmgr.ReadPIDFile(pidPath); err == nil {
		if procmgr.Alive(pid) {
			if procmgr.IsOurs(pid, filepath.Base(sp.SelfExe)) {
				fmt.Fprintf(logw, "static server already running (pid %d, %s)\n", pid, sp.Addr)
				return nil
			}
			return fmt.Errorf("pid %d in %s is not gitwo; refusing to start (port %s conflict?)", pid, pidPath, sp.Addr)
		}
		_ = os.Remove(pidPath)
	}

	_, portStr, err := net.SplitHostPort(sp.Addr)
	if err != nil {
		return fmt.Errorf("static_server.addr %q: %w", sp.Addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return fmt.Errorf("static_server.addr %q: bad port %q", sp.Addr, portStr)
	}
	if err := procmgr.CheckPort(port); err != nil {
		return fmt.Errorf("static server %s: %w", sp.Addr, err)
	}

	script := fmt.Sprintf(
		"nohup %s serve-static -addr %s -dir %s -spa=%t >> %s 2>&1 &\necho $! > %s\n",
		procmgr.QuoteSh(sp.SelfExe), procmgr.QuoteSh(sp.Addr), procmgr.QuoteSh(sp.Dir),
		sp.SPA, procmgr.QuoteSh(sp.LogPath), procmgr.QuoteSh(pidPath),
	)
	fmt.Fprintf(logw, "$ sh -c %q\n", script)
	cmd := exec.Command("sh", "-c", script)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		fmt.Fprintf(logw, "%s\n", strings.TrimRight(string(out), "\n"))
	}
	if err != nil {
		return fmt.Errorf("start static server failed: %w", err)
	}

	var pid int
	for i := 0; i < 20; i++ {
		pid, err = procmgr.ReadPIDFile(pidPath)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("pid file %s not written: %w", pidPath, err)
	}
	if !procmgr.WaitAlive(pid, 1*time.Second) {
		fmt.Fprintln(logw, "static server died immediately; last log lines:")
		fmt.Fprintln(logw, fsutil.TailFile(sp.LogPath, 2048))
		return fmt.Errorf("static server exited right after start, see %s", sp.LogPath)
	}
	fmt.Fprintf(logw, "static server started (pid %d) on %s serving %s\n", pid, sp.Addr, sp.Dir)
	return nil
}
