package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitwo/internal/auth"
)

// postFormH 用完整 Handler 提交表单（带登录会话），用于回归测试路由方法匹配。
func postFormH(t *testing.T, h http.Handler, u *UI, user, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Sessions.New(user)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRepoSettingsFormsUseFullName 回归：设置页两个表单必须提交到
// /repos/<owner>/<name>/settings（FullName），而不是 /repos/<name>/settings——
// 后者会命中 GET /repos/{owner}/{name} 路由，导致 POST 返回 405 Method Not Allowed。
func TestRepoSettingsFormsUseFullName(t *testing.T) {
	ui, _, _ := newBrowserUI(t)
	h := ui.Handler()
	req := httptest.NewRequest("GET", "/repos/admin/demo/settings", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: ui.Sessions.New("admin")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings page: status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/repos/admin/demo/settings"`) {
		t.Errorf("settings forms must post to /repos/admin/demo/settings (FullName)")
	}
	if strings.Contains(body, `action="/repos/demo/settings"`) {
		t.Errorf("settings form still posts to /repos/demo/settings (Name only) -> 405")
	}
}

// TestRepoSettingsUpdateAndDelete 走完整路由：保存设置、删除仓库都应成功。
func TestRepoSettingsUpdateAndDelete(t *testing.T) {
	ui, _, _ := newBrowserUI(t)
	h := ui.Handler()

	rec := postFormH(t, h, ui, "admin", "/repos/admin/demo/settings", "action=update&visibility=private&deploy_branch=main")
	if rec.Code != http.StatusFound {
		t.Fatalf("update settings: status %d (%s)", rec.Code, rec.Body.String())
	}
	rp, ok := ui.Store.GetRepo("admin/demo")
	if !ok {
		t.Fatal("repo disappeared after update")
	}
	if rp.Public || rp.DeployBranch != "main" {
		t.Fatalf("settings not applied: public=%v branch=%q", rp.Public, rp.DeployBranch)
	}

	rec = postFormH(t, h, ui, "admin", "/repos/admin/demo/settings", "action=delete")
	if rec.Code != http.StatusFound {
		t.Fatalf("delete repo: status %d (%s)", rec.Code, rec.Body.String())
	}
	if _, ok := ui.Store.GetRepo("admin/demo"); ok {
		t.Fatal("repo still exists after delete")
	}

	// 已删除仓库的设置页应 404
	req := httptest.NewRequest("GET", "/repos/admin/demo/settings", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: ui.Sessions.New("admin")})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted repo settings: status %d, want 404", rec.Code)
	}
}
