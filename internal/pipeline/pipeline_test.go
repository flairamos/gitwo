package pipeline

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gitwo/internal/deploycfg"
	"gitwo/internal/model"
)

// TestBuildFrontendNoBuildMode verifies the plain-static path: an empty
// command skips the build shell entirely and just validates/publishes the
// output directory.
func TestBuildFrontendNoBuildMode(t *testing.T) {
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "index.html"), []byte("<html>ok</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(ws, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "assets", "app.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := &Pipeline{Params: Params{TmpRoot: t.TempDir()}}
	tk := &model.Task{ID: model.NewID(), Repo: "r", Workdir: ws}

	dc := &deploycfg.Deploy{Type: deploycfg.TypeStatic,
		Build: deploycfg.Build{Frontend: &deploycfg.Frontend{Command: "", Output: "."}}}
	var w bytes.Buffer
	src, err := p.buildFrontend(tk, &w, dc, nil)
	if err != nil {
		t.Fatalf("no-build mode failed: %v", err)
	}
	if src != ws {
		t.Errorf("src = %q, want workspace root %q", src, ws)
	}
	if !bytes.Contains(w.Bytes(), []byte("跳过构建")) {
		t.Error("expected skip-build note in log output")
	}

	// output dir missing -> clear failure
	dc.Build.Frontend.Output = "dist"
	if _, err := p.buildFrontend(tk, &w, dc, nil); err == nil {
		t.Fatal("missing output dir should fail")
	}
}

// TestBuildFrontendWithBuild runs a real (trivial) build command to confirm
// the with-build path still executes shell commands.
func TestBuildFrontendWithBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell unavailable on windows; the with-build path is covered by the Linux smoke test")
	}
	ws := t.TempDir()
	p := &Pipeline{Params: Params{
		TmpRoot:  t.TempDir(),
		Timeouts: Timeouts{Build: time.Minute},
	}}
	tk := &model.Task{ID: model.NewID(), Repo: "r", Workdir: ws}

	dc := &deploycfg.Deploy{Type: deploycfg.TypeStatic,
		Build: deploycfg.Build{Frontend: &deploycfg.Frontend{
			Command: "mkdir -p dist && echo hi > dist/index.html",
			Output:  "dist",
		}}}
	var w bytes.Buffer
	src, err := p.buildFrontend(tk, &w, dc, nil)
	if err != nil {
		t.Fatalf("build mode failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "index.html")); err != nil {
		t.Fatalf("built output missing: %v", err)
	}
}

// TestNotifyWebhook：部署结束后向配置的 webhook POST 结果摘要。
func TestNotifyWebhook(t *testing.T) {
	received := make(chan []byte, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Gitwo-Event") != "deploy.finished" {
			t.Errorf("missing event header")
		}
		received <- body
	}))
	defer hook.Close()

	p := &Pipeline{Params: Params{NotifyWebhook: hook.URL}, Log: slog.Default()}
	task := &model.Task{
		ID: "t1", Repo: "team/app", Branch: "main", SHA: "abc",
		Status: model.StatusSuccess, FinishedAt: time.Now(),
	}
	p.notifyWebhook(task)

	select {
	case body := <-received:
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("bad payload: %v", err)
		}
		if m["status"] != "success" || m["repo"] != "team/app" {
			t.Errorf("payload = %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("webhook not called")
	}

	// 未配置 webhook 时不发送（直接返回，不 panic）
	p2 := &Pipeline{Params: Params{}, Log: slog.Default()}
	p2.notifyWebhook(task)
}

// TestNotifyWebhookSkippedStates：pending/skipped 状态不通知。
func TestNotifyWebhookSkippedStates(t *testing.T) {
	called := false
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer hook.Close()
	p := &Pipeline{Params: Params{NotifyWebhook: hook.URL}, Log: slog.Default()}
	task := &model.Task{ID: "t2", Status: model.StatusPending}
	p.notifyWebhook(task)
	if called {
		t.Error("webhook fired for non-terminal status")
	}
}
