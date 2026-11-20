package webui

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitwo/internal/appconfig"
	"gitwo/internal/auth"
	"gitwo/internal/pipeline"
	"gitwo/internal/store"
)

// writeAppLog 造一个含 n 行的 app.log。
func writeAppLog(t *testing.T, n int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.log")
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %04d\n", i)
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestReadAppLogPagePagination：按字节游标从尾部向前翻页，不漏行、不重复。
func TestReadAppLogPagePagination(t *testing.T) {
	path := writeAppLog(t, 100)
	pg, err := readAppLogPage(path, 0, 10, appLogPageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Lines) != 10 || pg.Lines[9] != "line 0100" || pg.Lines[0] != "line 0091" {
		t.Fatalf("first page = %v", pg.Lines)
	}
	if !pg.HasMore || pg.Start <= 0 {
		t.Fatalf("first page cursor: hasMore=%v start=%d", pg.HasMore, pg.Start)
	}
	seen := append([]string{}, pg.Lines...)
	before := pg.Start
	for pg.HasMore {
		pg, err = readAppLogPage(path, before, 10, appLogPageBytes)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(pg.Lines, seen...)
		before = pg.Start
		if len(seen) > 200 {
			t.Fatal("pagination does not terminate")
		}
	}
	if len(seen) != 100 {
		t.Fatalf("paged %d lines, want 100", len(seen))
	}
	for i, ln := range seen {
		if want := fmt.Sprintf("line %04d", i+1); ln != want {
			t.Fatalf("line %d = %q, want %q", i, ln, want)
		}
	}
}

// TestReadAppLogPageEdges：无结尾换行 / 空文件 / 不存在 / before 超界。
func TestReadAppLogPageEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(path, []byte("line 0001\nline 0002\ntail-without-newline"), 0o644); err != nil {
		t.Fatal(err)
	}
	pg, err := readAppLogPage(path, 0, 10, appLogPageBytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(pg.Lines) != 3 || pg.Lines[2] != "tail-without-newline" {
		t.Fatalf("no-trailing-newline page = %v", pg.Lines)
	}

	empty := filepath.Join(t.TempDir(), "app.log")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	pg, err = readAppLogPage(empty, 0, 10, appLogPageBytes)
	if err != nil || len(pg.Lines) != 0 || pg.HasMore || pg.Start != 0 {
		t.Fatalf("empty log: %+v err=%v", pg, err)
	}

	if _, err := readAppLogPage(filepath.Join(t.TempDir(), "missing.log"), 0, 10, appLogPageBytes); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing log err = %v, want fs.ErrNotExist", err)
	}

	// before 大于文件大小：视为读最新一页
	pg, err = readAppLogPage(path, 1<<30, 10, appLogPageBytes)
	if err != nil || len(pg.Lines) != 3 {
		t.Fatalf("oversized before: %+v err=%v", pg, err)
	}
}

// logTestUI 构造一个只用于日志接口的 UI：私有仓库 admin/demo + apps 目录里的 app.log。
func logTestUI(t *testing.T) *UI {
	t.Helper()
	home := appconfig.Home{Dir: t.TempDir()}
	if err := home.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(home.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRepo("demo", "user:admin", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser("dev", "pw-123456", false, store.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(home.AppsRoot(), "admin_demo")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appDir, "app.log"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &UI{
		Home:     home,
		Store:    st,
		Sessions: auth.NewSessions(),
		Pipe:     &pipeline.Pipeline{Params: pipeline.Params{AppsRoot: home.AppsRoot()}},
	}
}

func logReq(api string) *http.Request {
	req := httptest.NewRequest("GET", api, nil)
	req.SetPathValue("owner", "admin")
	req.SetPathValue("name", "demo")
	return req
}

// TestAppLogPermissions：匿名重定向登录、测试角色 403、非成员 403、管理员 200，
// 且响应只含本仓库日志。
func TestAppLogPermissions(t *testing.T) {
	ui := logTestUI(t)
	adminToken := ui.Sessions.New("admin")
	devToken := ui.Sessions.New("dev")
	qaToken := ui.Sessions.New("qa")

	check := func(token string, wantCode int, label string) *httptest.ResponseRecorder {
		req := logReq("/api/repos/admin/demo/logs")
		if token != "" {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		}
		rec := httptest.NewRecorder()
		ui.repoLogsAPI(rec, req)
		if rec.Code != wantCode {
			t.Fatalf("%s: status %d, want %d (%s)", label, rec.Code, wantCode, rec.Body.String())
		}
		return rec
	}

	check("", http.StatusFound, "anonymous")                      // 未登录 → 跳转 /login
	check(qaToken, http.StatusForbidden, "tester")                // 测试角色看不到日志
	check(devToken, http.StatusForbidden, "non-member developer") // 非成员
	rec := check(adminToken, http.StatusOK, "admin")
	if !strings.Contains(rec.Body.String(), "hello") || !strings.Contains(rec.Body.String(), "world") {
		t.Fatalf("admin body = %s", rec.Body.String())
	}

	// 页面：管理员可打开，匿名跳登录
	rec = httptest.NewRecorder()
	ui.repoLogsPage(rec, logReq("/repos/admin/demo/logs"))
	if rec.Code != http.StatusFound {
		t.Fatalf("anonymous page status = %d, want 302", rec.Code)
	}
	req := logReq("/repos/admin/demo/logs")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: adminToken})
	rec = httptest.NewRecorder()
	ui.repoLogsPage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin page status = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "运行日志") {
		t.Errorf("page missing app.log section")
	}
}

// TestAppLogMissingFile：没有 app.log 时 API 返回 404 + exists=false，页面仍 200 显示提示。
func TestAppLogMissingFile(t *testing.T) {
	ui := logTestUI(t)
	if err := os.Remove(filepath.Join(ui.Home.AppsRoot(), "admin_demo", "app.log")); err != nil {
		t.Fatal(err)
	}
	token := ui.Sessions.New("admin")
	req := logReq("/api/repos/admin/demo/logs")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	ui.repoLogsAPI(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"exists":false`) {
		t.Fatalf("missing log API: %d %s", rec.Code, rec.Body.String())
	}

	req = logReq("/repos/admin/demo/logs")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	rec = httptest.NewRecorder()
	ui.repoLogsPage(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "还没有 app.log") {
		t.Fatalf("missing log page: %d", rec.Code)
	}
}
