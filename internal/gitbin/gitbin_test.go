package gitbin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func failLookPath(string) (string, error) { return "", errors.New("not in PATH") }

// TestLookupFallback：PATH 中没有时按候选路径探测。
func TestLookupFallback(t *testing.T) {
	f := filepath.Join(t.TempDir(), "git-fake")
	if err := os.WriteFile(f, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := lookup(failLookPath, []string{filepath.Join(t.TempDir(), "nope"), f})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got != f {
		t.Errorf("lookup = %q, want %q", got, f)
	}
}

// TestLookupPrefersPath：PATH 命中优先于候选路径。
func TestLookupPrefersPath(t *testing.T) {
	got, err := lookup(func(string) (string, error) { return "/usr/bin/git", nil }, []string{"/nonexistent"})
	if err != nil || got != "/usr/bin/git" {
		t.Fatalf("lookup = %q, %v", got, err)
	}
}

// TestLookupNotFound：全部失败时错误信息要可操作（提到安装/PATH）。
func TestLookupNotFound(t *testing.T) {
	_, err := lookup(failLookPath, []string{filepath.Join(t.TempDir(), "nope")})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"git", "PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// TestCandidatesPlatform：当前平台应探测到至少一个候选（Windows Git 路径或 Unix 路径）。
func TestCandidatesPlatform(t *testing.T) {
	if len(candidates()) == 0 {
		t.Fatal("no candidates")
	}
}

// TestResolveExplicit：显式路径优先（对应 server.yaml 的 git_path / GITWO_GIT），
// 路径不可用时给出明确错误。
func TestResolveExplicit(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "git-fake")
	if err := os.WriteFile(f, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 重置包级缓存，隔离测试
	mu.Lock()
	resolved = false
	path = ""
	err = nil
	mu.Unlock()
	if e := Resolve(f); e != nil {
		t.Fatalf("Resolve(explicit): %v", e)
	}
	if Path() != f {
		t.Errorf("Path() = %q, want %q", Path(), f)
	}
	mu.Lock()
	resolved = false
	path = ""
	err = nil
	mu.Unlock()
	if e := Resolve(filepath.Join(dir, "missing")); e == nil {
		t.Fatal("missing explicit path should error")
	}
}

// ---- 自动安装（Ensure） ----

// TestEnsureExplicitSkipsInstall：显式路径可用时不触发安装。
func TestEnsureExplicitSkipsInstall(t *testing.T) {
	t.Setenv("GITWO_GIT", "")
	dir := t.TempDir()
	f := filepath.Join(dir, "git-fake")
	os.WriteFile(f, []byte("x"), 0o755)
	old := install
	called := false
	install = func() error { called = true; return nil }
	t.Cleanup(func() { install = old })
	reset()
	if err := Ensure(f, true); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if called {
		t.Error("install should not be called when git resolves")
	}
}

// TestEnsureAutoDisabled：auto=false 时直接给手动安装指引。
func TestEnsureAutoDisabled(t *testing.T) {
	t.Setenv("GITWO_GIT", "")
	old := install
	called := false
	install = func() error { called = true; return nil }
	t.Cleanup(func() { install = old })
	reset()
	err := Ensure(filepath.Join(t.TempDir(), "missing"), false)
	if err == nil || !strings.Contains(err.Error(), "安装 git") {
		t.Fatalf("err = %v, want manual install hint", err)
	}
	if called {
		t.Error("install should not be called when disabled")
	}
}

// TestEnsureInstallFailsOffline：安装失败（无网络/无权限）时报错含手动指引。
func TestEnsureInstallFailsOffline(t *testing.T) {
	t.Setenv("GITWO_GIT", "")
	old := install
	t.Cleanup(func() { install = old })
	reset()
	install = func() error { return errors.New("network unreachable") }
	err := Ensure(filepath.Join(t.TempDir(), "missing"), true)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"自动安装 git 失败", "network unreachable", "安装 git"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q missing %q", err, want)
		}
	}
}

// TestEnsureInstallSuccess：安装成功后重新探测到 git。
func TestEnsureInstallSuccess(t *testing.T) {
	t.Setenv("GITWO_GIT", "")
	dir := t.TempDir()
	target := filepath.Join(dir, "git-fake")
	old := install
	t.Cleanup(func() { install = old })
	reset()
	install = func() error {
		return os.WriteFile(target, []byte("x"), 0o755) // 模拟安装器把 git 放到 target
	}
	if err := Ensure(target, true); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if Path() != target {
		t.Errorf("Path() = %q, want %q", Path(), target)
	}
}
