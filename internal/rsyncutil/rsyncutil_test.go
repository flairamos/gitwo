package rsyncutil

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestMirrorDirKeepsRoot 回归测试：--delete 清理遍历曾把 dst 根目录本身当成
// "多余条目"删光（根目录不在 keep 里），导致刚发布的站点整个消失。
func TestMirrorDirKeepsRoot(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	write := func(base, rel, content string) {
		t.Helper()
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(src, "index.html", "<h1>hello</h1>")
	write(src, "assets/app.css", "body{}")

	var log bytes.Buffer
	if err := mirrorDir(context.Background(), src, dst, &log); err != nil {
		t.Fatalf("mirrorDir: %v\n%s", err, log.String())
	}

	// 复制的内容必须保留（回归：曾连目录一起被删）
	for _, rel := range []string{"index.html", "assets/app.css"} {
		data, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("mirrored file %s missing: %v", rel, err)
			continue
		}
		if rel == "index.html" && string(data) != "<h1>hello</h1>" {
			t.Errorf("index.html content = %q", data)
		}
	}

	// 第二次 mirror：dst 里的多余文件被清理，src 文件仍在
	write(dst, "stale/old.txt", "old")
	if err := mirrorDir(context.Background(), src, dst, &log); err != nil {
		t.Fatalf("mirrorDir 2: %v\n%s", err, log.String())
	}
	if _, err := os.Stat(filepath.Join(dst, "stale")); !os.IsNotExist(err) {
		t.Error("stale dir should be removed by --delete")
	}
	if _, err := os.Stat(filepath.Join(dst, "index.html")); err != nil {
		t.Error("index.html should survive the second mirror")
	}
}

// TestSyncSkipsRepoMetadata：仓库元数据（.git / deploy.yaml）不进发布目录。
func TestSyncSkipsRepoMetadata(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(src, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "hi")
	write(".git/config", "[core]")
	write("deploy.yaml", "type: static")

	var log bytes.Buffer
	// 强制走内置 mirror：Available() 在有 rsync 的机器上会走 rsync 路径，
	// 两者语义一致；这里直接测 mirrorDir 保证覆盖。
	if err := mirrorDir(context.Background(), src, dst, &log); err != nil {
		t.Fatalf("mirrorDir: %v\n%s", err, log.String())
	}
	for _, banned := range []string{".git", "deploy.yaml"} {
		if _, err := os.Stat(filepath.Join(dst, banned)); !os.IsNotExist(err) {
			t.Errorf("%s must not be published", banned)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "index.html")); err != nil {
		t.Error("index.html missing")
	}
}
