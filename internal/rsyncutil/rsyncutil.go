// Package rsyncutil wraps the system rsync binary used to publish frontend
// build output to its target directory.
package rsyncutil

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gitwo/internal/shellutil"
)

// Available reports a clear error when rsync is missing.
func Available() error {
	if _, err := exec.LookPath("rsync"); err != nil {
		return fmt.Errorf("rsync not found in PATH (install it: `apt install rsync` on Ubuntu, `dnf install rsync` on CentOS Stream)")
	}
	return nil
}

// publishExcludes keeps repository metadata out of published sites when a
// static site is published straight from the workspace (output = repo root).
var publishExcludes = []string{"/.git", "/deploy.yaml", "/deploy.yml"}

// Sync mirrors srcDir into dstDir: rsync -a --delete when the rsync binary
// exists (preferred on Linux), otherwise a built-in pure-Go mirror with the
// same semantics (add/update/delete) so the platform stays self-contained.
// Repository metadata (.git, deploy.yaml) is never published.
func Sync(ctx context.Context, srcDir, dstDir string, logw io.Writer) error {
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return fmt.Errorf("create target dir %s: %w", dstDir, err)
	}
	if err := Available(); err == nil {
		argv := []string{"rsync", "-a", "--delete"}
		for _, e := range publishExcludes {
			argv = append(argv, "--exclude="+e)
		}
		argv = append(argv, filepath.Clean(srcDir)+"/", filepath.Clean(dstDir)+"/")
		fmt.Fprintf(logw, "$ %s\n", strings.Join(argv, " "))
		return shellutil.Run(ctx, "", nil, logw, argv...)
	}
	fmt.Fprintf(logw, "$ mirror (built-in, rsync not found) %s -> %s\n", srcDir, dstDir)
	return mirrorDir(ctx, srcDir, dstDir, logw)
}

// mirrorDir copies src into dst preserving mtimes/permissions and removes
// extraneous files in dst — the -a --delete subset rsync gives us. Top-level
// repository metadata (.git, deploy.yaml) is skipped like the rsync excludes.
func mirrorDir(ctx context.Context, src, dst string, logw io.Writer) error {
	keep := make(map[string]bool) // dst 内应保留条目的绝对路径
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			return filepath.SkipDir
		}
		if rel == "deploy.yaml" || rel == "deploy.yml" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		keep[target] = true
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil // skip symlinks/special files
		}
		existing, serr := os.Stat(target)
		if serr == nil && existing.Size() == info.Size() && existing.ModTime().Equal(info.ModTime()) {
			return nil // unchanged
		}
		return copyFile(p, target, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	// --delete: remove anything in dst not present in src。
	// dst 根目录本身永远保留：它不在 keep 里（rel=="." 被跳过），
	// 若不排除，整个发布目录会被自己删光。
	var remove []string
	_ = filepath.Walk(dst, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if p == dst {
			return nil
		}
		if !keep[p] {
			remove = append(remove, p)
			if info.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	sort.Slice(remove, func(i, j int) bool { return len(remove[i]) > len(remove[j]) }) // children first
	for _, p := range remove {
		if err := os.RemoveAll(p); err != nil {
			fmt.Fprintf(logw, "warning: remove %s: %v\n", p, err)
		}
	}
	fmt.Fprintf(logw, "mirror complete: %s -> %s\n", src, dst)
	return nil
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if st, err := os.Stat(src); err == nil {
		_ = os.Chtimes(dst, st.ModTime(), st.ModTime())
	}
	return nil
}

// CopyFile copies a single file preserving permissions and mtime.
func CopyFile(src, dst string) error {
	st, err := os.Stat(src)
	if err != nil {
		return err
	}
	return copyFile(src, dst, st.Mode().Perm())
}

// VerifyIndex fails the deployment when the published site has no index.html
// (almost always means the build output path is wrong).
func VerifyIndex(dir string) error {
	st, err := os.Stat(filepath.Join(dir, "index.html"))
	if err != nil {
		return fmt.Errorf("index.html missing in %s (wrong build.frontend.output or empty dist?)", dir)
	}
	if st.IsDir() {
		return fmt.Errorf("%s/index.html is a directory", dir)
	}
	return nil
}
