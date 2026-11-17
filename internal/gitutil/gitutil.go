// Package gitutil synchronizes the fixed local workspace of a repo with the
// exact commit reported by the webhook: clone once, then fetch + checkout
// the SHA so every deployment is traceable to a commit.
package gitutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gitwo/internal/gitbin"
	"gitwo/internal/shellutil"
)

// Sync ensures dir is a git clone of url checked out at branch/commit.
// sha may be empty (manual deploys): then the branch head is checked out.
// It returns the exact checked-out commit SHA.
func Sync(ctx context.Context, cloneURL, dir, branch, sha string, env []string, logw io.Writer) (string, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", fmt.Errorf("create workspace parent: %w", err)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0") // never hang waiting for credentials

	display := redactURL(cloneURL)
	run := func(cwd string, args ...string) error {
		cmdline := strings.Join(args, " ")
		fmt.Fprintf(logw, "$ git %s\n", strings.ReplaceAll(cmdline, cloneURL, display))
		argv := append([]string{gitbin.Path()}, args...)
		return shellutil.Run(ctx, cwd, env, logw, argv...)
	}

	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); errors.Is(err, fs.ErrNotExist) {
		// First deploy: clone. If dir exists but is not a repo, fail loudly
		// instead of deleting user data.
		if st, serr := os.Stat(dir); serr == nil && !st.IsDir() {
			return "", fmt.Errorf("workspace %s exists and is not a directory", dir)
		}
		if err := run(filepath.Dir(dir), "clone", cloneURL, dir); err != nil {
			return "", fmt.Errorf("git clone failed: %w", err)
		}
	} else if err != nil {
		return "", err
	}

	// Refresh remote URL so credential changes take effect without a re-clone.
	if err := run(dir, "remote", "set-url", "origin", cloneURL); err != nil {
		return "", fmt.Errorf("git remote set-url failed: %w", err)
	}
	if err := run(dir, "fetch", "--prune", "origin", branch); err != nil {
		return "", fmt.Errorf("git fetch origin %s failed: %w", branch, err)
	}

	if sha != "" {
		if err := run(dir, "checkout", "-f", "--detach", sha); err != nil {
			return "", fmt.Errorf("git checkout %s failed (commit missing from branch %s? force-pushed?): %w", short(sha), branch, err)
		}
	} else {
		if err := run(dir, "checkout", "-f", "-B", branch, "origin/"+branch); err != nil {
			return "", fmt.Errorf("git checkout %s failed: %w", branch, err)
		}
	}
	// Drop untracked build artifacts so every build starts reproducible.
	if err := run(dir, "clean", "-fdx"); err != nil {
		return "", fmt.Errorf("git clean failed: %w", err)
	}

	rev, err := shellutil.Output(ctx, dir, env, logw, gitbin.Path(), "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	rev = strings.TrimSpace(rev)
	if sha != "" && rev != sha {
		return "", fmt.Errorf("checked out %s but expected %s", short(rev), short(sha))
	}
	return rev, nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[url]"
	}
	if u.User != nil {
		u.User = url.User("REDACTED")
	}
	return u.String()
}
