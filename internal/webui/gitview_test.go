package webui

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"gitwo/internal/appconfig"
	"gitwo/internal/auth"
	"gitwo/internal/history"
	"gitwo/internal/model"
	"gitwo/internal/pipeline"
	"gitwo/internal/queue"
	"gitwo/internal/store"
)

// safeRef/safePath 守护所有 git 调用的参数——重点防 option 注入（以 "-" 开头，
// 例如 git log --output=/path/evil）与路径穿越。
func TestSafeRef(t *testing.T) {
	valid := []string{"main", "master", "feature/x", "v1.2.3", "release-1.0", "a1b2c3d", "user/topic_1"}
	for _, s := range valid {
		if !safeRef(s) {
			t.Errorf("safeRef(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"--output=/tmp/evil", // git option injection
		"-h",
		"main..other",      // range syntax
		"../../etc/passwd", // traversal
		"",                 // empty
		"a b",              // whitespace
		"a;b",              // metacharacters
		"a|b", "a&b", "a$b", "a`b",
		"a'b", `a"b`, "a\nb", "a\tb",
	}
	for _, s := range invalid {
		if safeRef(s) {
			t.Errorf("safeRef(%q) = true, want false", s)
		}
	}
}

func TestSafePath(t *testing.T) {
	valid := []string{"src/index.js", "README.md", "assets/style.css", "docs/使用说明.md", "a b/file name.txt"}
	for _, s := range valid {
		if !safePath(s) {
			t.Errorf("safePath(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"../../etc/passwd",
		"..",
		"src/../../x",
		"-flag",
		"src;/x", "src|x", "src&x", "src$x", "src`x", "src'x", `src"x`,
		"src<x", "src>x", "src\\x", "src\nx", "src\tx",
	}
	for _, s := range invalid {
		if safePath(s) {
			t.Errorf("safePath(%q) = true, want false", s)
		}
	}
}

// ---- file browser (tree/blob/raw) ----

// newBrowserUI 构造一个带真实 git 仓库（admin/demo，public，分支 main+dev）
// 的最小 UI，返回 UI、挂好 tree/blob/raw 路由的 mux（用于让 PathValue 生效）
// 与裸仓库路径。
func newBrowserUI(t *testing.T) (*UI, *http.ServeMux, string) {
	t.Helper()
	home := appconfig.Home{Dir: t.TempDir()}
	if err := home.EnsureDirs(); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	st, err := store.Load(home.DataDir())
	if err != nil {
		t.Fatalf("load store: %v", err)
	}
	// store.Load 已初始化默认 admin 账户，直接使用
	if err := st.CreateRepo("demo", "user:admin", true, ""); err != nil {
		t.Fatalf("create repo: %v", err)
	}

	// 裸仓库 + 一次提交（含子目录与二进制文件）
	bare := filepath.Join(home.ReposDir(), "admin", "demo.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_ = exec.Command("git", "--git-dir", bare, "symbolic-ref", "HEAD", "refs/heads/main").Run()

	work := filepath.Join(t.TempDir(), "work")
	if out, err := exec.Command("git", "init", "--quiet", work).CombinedOutput(); err != nil {
		t.Fatalf("git init work: %v\n%s", err, out)
	}
	gitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitIn(work, "symbolic-ref", "HEAD", "refs/heads/main")
	gitIn(work, "config", "user.email", "t@example.com")
	gitIn(work, "config", "user.name", "t")
	write := func(rel, content string) {
		p := filepath.Join(work, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# demo\nhello gitwo\n")
	write("src/main.go", "package main\n\nfunc main() {}\n")
	write("bin/data.bin", "OK\x00BIN\x00ARY")
	gitIn(work, "add", "-A")
	gitIn(work, "commit", "--quiet", "-m", "init")
	gitIn(work, "push", "--quiet", bare, "main")

	// 第二个分支 dev：多分支浏览用
	gitIn(work, "checkout", "-q", "-b", "dev")
	write("feature.txt", "feature on dev\n")
	gitIn(work, "add", "-A")
	gitIn(work, "commit", "--quiet", "-m", "dev work")
	gitIn(work, "push", "--quiet", bare, "dev")

	u := &UI{
		Home:  home,
		Cfg:   &appconfig.Config{Listen: ":9527"},
		Store: st,
		Pipe: &pipeline.Pipeline{Params: pipeline.Params{
			AppsRoot: home.AppsRoot(),
			LogRoot:  filepath.Join(home.Dir, "logs"),
		}},
		Log: slog.Default(),
	}
	u.Sessions = auth.NewSessions()
	hist, err := history.New(200, filepath.Join(home.DataDir(), "history.jsonl"))
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	u.Hist = hist
	u.Queue = queue.New(1, 5, func(*model.Task) {}) // noop 执行：入队即可，不真构建

	mux := http.NewServeMux()
	mux.HandleFunc("GET /me", u.mePage)
	mux.HandleFunc("GET /me/keys", u.meKeysPage)
	mux.HandleFunc("POST /me/keys/add", u.meKeysAddPost)
	mux.HandleFunc("POST /me/keys/{id}/delete", u.meKeysDeletePost)
	mux.HandleFunc("GET /repos/{owner}/{name}/tree/{branch}", u.repoTree)
	mux.HandleFunc("GET /repos/{owner}/{name}/blob/{branch}", u.repoBlob)
	mux.HandleFunc("GET /repos/{owner}/{name}/raw/{branch}", u.repoRaw)
	mux.HandleFunc("GET /repos/{owner}/{name}/tree/{branch}/{path...}", u.repoTree)
	mux.HandleFunc("GET /repos/{owner}/{name}/blob/{branch}/{path...}", u.repoBlob)
	mux.HandleFunc("GET /repos/{owner}/{name}/raw/{branch}/{path...}", u.repoRaw)
	mux.HandleFunc("POST /repos/{owner}/{name}/deploy", u.repoDeployPost)
	mux.HandleFunc("GET /{$}", u.dashboard)
	mux.HandleFunc("GET /repos/new", u.repoNewPage)
	mux.HandleFunc("GET /repos/{owner}/{name}", u.repoOverview)
	mux.HandleFunc("GET /repos/{owner}/{name}/commits/{branch}", u.repoCommits)
	mux.HandleFunc("GET /pipelines", u.pipelinesPage)
	mux.HandleFunc("GET /pipelines/{id}", u.pipelineDetail)
	mux.HandleFunc("GET /api/tasks/{id}/log", u.taskLogAPI)
	mux.HandleFunc("GET /api/status", u.statusAPI)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs", u.repoBugsPage)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs/new", u.bugNewPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/bugs/new", u.bugNewPost)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs/{id}", u.bugDetail)
	mux.HandleFunc("POST /repos/{owner}/{name}/bugs/{id}/status", u.bugStatusPost)
	mux.HandleFunc("POST /repos/{owner}/{name}/bugs/{id}/comment", u.bugCommentPost)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs/{id}/files/{file}", u.bugFileServe)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs", u.repoReqsPage)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs/new", u.reqNewPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/reqs/new", u.reqCreatePost)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs/{id}", u.reqDetail)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs/{id}/edit", u.reqEditPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/reqs/{id}/edit", u.reqEditPost)
	mux.HandleFunc("POST /repos/{owner}/{name}/reqs/{id}/status", u.reqStatusPost)
	mux.HandleFunc("/api/{slug}", u.apiProxy)
	mux.HandleFunc("/api/{slug}/{path...}", u.apiProxy)
	return u, mux, bare
}

func getBrowser(t *testing.T, mux *http.ServeMux, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	return rec
}

func TestFileBrowserHandlers(t *testing.T) {
	_, mux, bare := newBrowserUI(t)

	// 根目录列表：目录优先、文件随后
	rec := getBrowser(t, mux, "/repos/admin/demo/tree/main")
	if rec.Code != 200 {
		t.Fatalf("tree root: status %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"bin", "src", "README.md"} {
		if !strings.Contains(body, want) {
			t.Errorf("tree root missing %q", want)
		}
	}
	if idxBin, idxReadme := strings.Index(body, `tree/main/bin`), strings.Index(body, `blob/main/README.md`); idxBin == -1 || idxReadme == -1 || idxBin > idxReadme {
		t.Errorf("dirs should sort before files (bin@%d README@%d)", idxBin, idxReadme)
	}

	// 子目录
	rec = getBrowser(t, mux, "/repos/admin/demo/tree/main/src")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "main.go") {
		t.Fatalf("tree src: status %d", rec.Code)
	}
	rec = getBrowser(t, mux, "/repos/admin/demo/tree/main/bin")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data.bin") {
		t.Fatalf("tree bin: status %d", rec.Code)
	}

	// blob：文本内容 + Raw 链接
	rec = getBrowser(t, mux, "/repos/admin/demo/blob/main/README.md")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hello gitwo") {
		t.Fatalf("blob README: status %d\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `/raw/main/README.md`) {
		t.Error("blob page missing raw link")
	}

	// raw：逐字节一致 + 文本类型
	rec = getBrowser(t, mux, "/repos/admin/demo/raw/main/README.md")
	if rec.Code != 200 {
		t.Fatalf("raw README: status %d", rec.Code)
	}
	if got := rec.Body.String(); got != "# demo\nhello gitwo\n" {
		t.Errorf("raw body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("raw text content-type = %q", ct)
	}

	// raw：二进制类型 + 内容一致
	rec = getBrowser(t, mux, "/repos/admin/demo/raw/main/bin/data.bin")
	if rec.Code != 200 {
		t.Fatalf("raw bin: status %d", rec.Code)
	}
	if got := rec.Body.String(); got != "OK\x00BIN\x00ARY" {
		t.Errorf("raw binary body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/octet-stream") {
		t.Errorf("raw binary content-type = %q", ct)
	}

	// 多分支：dev 分支的文件可见，分支切换下拉出现
	rec = getBrowser(t, mux, "/repos/admin/demo/tree/dev")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "feature.txt") {
		t.Fatalf("tree dev: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<select") {
		t.Error("tree dev missing branch dropdown")
	}
	rec = getBrowser(t, mux, "/repos/admin/demo/blob/dev/feature.txt")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "feature on dev") {
		t.Fatalf("blob dev/feature.txt: status %d\n%s", rec.Code, rec.Body.String())
	}
	// 切换分支后原路径仍存在（两分支都有 src/）
	if rec = getBrowser(t, mux, "/repos/admin/demo/tree/dev/src"); rec.Code != 200 {
		t.Errorf("tree dev/src: status %d, want 200", rec.Code)
	}

	// 提交 sha 作为 ref 浏览（提交历史页的"浏览此提交"链接）
	out, err := exec.Command("git", "--git-dir", bare, "rev-parse", "refs/heads/main").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	sha := strings.TrimSpace(string(out))
	if rec = getBrowser(t, mux, "/repos/admin/demo/tree/"+sha); rec.Code != 200 || !strings.Contains(rec.Body.String(), "README.md") {
		t.Errorf("tree by sha: status %d", rec.Code)
	}

	// 目录/文件互跳
	if rec = getBrowser(t, mux, "/repos/admin/demo/blob/main/src"); rec.Code != http.StatusFound {
		t.Errorf("blob on dir: status %d, want 302 redirect to tree", rec.Code)
	}
	if rec = getBrowser(t, mux, "/repos/admin/demo/tree/main/README.md"); rec.Code != http.StatusFound {
		t.Errorf("tree on file: status %d, want 302 redirect to blob", rec.Code)
	}

	// 错误处理
	// 路径在该分支不存在（切换分支后路径消失的常见场景）：302 回到分支根目录
	rec = getBrowser(t, mux, "/repos/admin/demo/tree/main/nope")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "/tree/main") {
		t.Errorf("missing path: status %d location %q, want 302 -> /tree/main", rec.Code, rec.Header().Get("Location"))
	}
	if rec = getBrowser(t, mux, "/repos/admin/demo/tree/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("missing branch: status %d, want 404", rec.Code)
	}
	if rec = getBrowser(t, mux, "/repos/admin/demo/tree/--output=evil"); rec.Code != http.StatusBadRequest {
		t.Errorf("option injection branch: status %d, want 400", rec.Code)
	}
}

func TestCodeLang(t *testing.T) {
	cases := map[string]string{
		"main.go": "go", "app.js": "js", "a.ts": "ts", "m.py": "py", "X.java": "java",
		"core.c": "c", "x.hpp": "cpp", "lib.rs": "rust", "run.sh": "sh", "q.sql": "sql",
		"ci.yaml": "yaml", "ci.yml": "yaml", "pkg.json": "json", "Cargo.toml": "toml",
		"app.ini": "ini", "style.css": "css", "index.html": "html", "icon.svg": "html",
		"a.rb": "rb", "a.php": "php",
		"README": "", "data.bin": "", "noext": "",
	}
	for name, want := range cases {
		if got := codeLang(name); got != want {
			t.Errorf("codeLang(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseLsTree(t *testing.T) {
	out := "100644 blob abc123 25\tREADME.md\x00" +
		"040000 tree def456 -\tstatic\x00" +
		"100755 blob 789abc 1024\trun.sh\x00"
	entries := parseLsTree(out, "admin/demo", "main", "")
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	// 目录优先，其余按名称：static, README.md, run.sh
	wantOrder := []string{"static", "README.md", "run.sh"}
	for i, e := range entries {
		if e.Name != wantOrder[i] {
			t.Errorf("entry[%d] = %s, want %s", i, e.Name, wantOrder[i])
		}
	}
	if !entries[0].IsDir || entries[0].Size != "-" || entries[0].URL != "/repos/admin/demo/tree/main/static" {
		t.Errorf("dir entry wrong: %+v", entries[0])
	}
	if entries[1].Size != "25 B" || entries[1].URL != "/repos/admin/demo/blob/main/README.md" {
		t.Errorf("file entry wrong: %+v", entries[1])
	}
	if entries[2].Size != "1.0 KiB" {
		t.Errorf("run.sh size = %q, want 1.0 KiB", entries[2].Size)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		-1:         "-",
		0:          "0 B",
		999:        "999 B",
		1024:       "1.0 KiB",
		1024 * 5:   "5.0 KiB",
		1048576:    "1.0 MiB",
		1073741824: "1.0 GiB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

// ---- pipeline isolation（仓库只能进入自己的流水线） ----

// getBrowserAs 以指定用户身份发起请求（注入会话 Cookie）。
func getBrowserAs(t *testing.T, mux *http.ServeMux, u *UI, user, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Sessions.New(user)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPipelinesIsolation(t *testing.T) {
	u, mux, _ := newBrowserUI(t)

	// 参与者：mallory（私有仓库主）、eve（无任何仓库）
	if err := u.Store.CreateUser("mallory", "pw-123456", false, ""); err != nil {
		t.Fatalf("create mallory: %v", err)
	}
	if err := u.Store.CreateUser("eve", "pw-123456", false, ""); err != nil {
		t.Fatalf("create eve: %v", err)
	}
	if err := u.Store.CreateRepo("secret", "user:mallory", false, ""); err != nil {
		t.Fatalf("create secret: %v", err)
	}
	// demo 改为 private，使可见性隔离可测
	if err := u.Store.UpdateRepo("admin/demo", func(m *store.Repo) error {
		m.Public = false
		return nil
	}); err != nil {
		t.Fatalf("privatize demo: %v", err)
	}

	// 三个任务：admin/demo、mallory/secret、已删除仓库 ghost/gone
	t1 := u.Pipe.NewTask("admin/demo", "main", "a1b2c3")
	u.Hist.Add(t1)
	t2 := u.Pipe.NewTask("mallory/secret", "main", "d4e5f6")
	u.Hist.Add(t2)
	t3 := u.Pipe.NewTask("ghost/gone", "main", "070809")
	u.Hist.Add(t3)

	// 全局视图：admin 全量可见
	rec := getBrowserAs(t, mux, u, "admin", "/pipelines")
	if rec.Code != 200 {
		t.Fatalf("admin global: status %d", rec.Code)
	}
	for _, id := range []string{t1.ID, t2.ID, t3.ID} {
		if !strings.Contains(rec.Body.String(), id) {
			t.Errorf("admin global missing task %s", id)
		}
	}

	// 全局视图：mallory 只能看到自己仓库（demo 已私有、ghost 已删除）
	rec = getBrowserAs(t, mux, u, "mallory", "/pipelines")
	if rec.Code != 200 {
		t.Fatalf("mallory global: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), t2.ID) {
		t.Error("mallory should see own repo task")
	}
	for _, id := range []string{t1.ID, t3.ID} {
		if strings.Contains(rec.Body.String(), id) {
			t.Errorf("mallory must NOT see task %s", id)
		}
	}

	// 仓库作用域：无权者 403；有权者只见该仓库任务，且无"查看全部"入口
	if rec = getBrowserAs(t, mux, u, "mallory", "/pipelines?repo=admin/demo"); rec.Code != http.StatusForbidden {
		t.Errorf("scoped as non-reader: status %d, want 403", rec.Code)
	}
	rec = getBrowserAs(t, mux, u, "admin", "/pipelines?repo=admin/demo")
	if rec.Code != 200 {
		t.Fatalf("scoped as admin: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), t1.ID) || strings.Contains(rec.Body.String(), t2.ID) {
		t.Error("scoped view should contain only admin/demo tasks")
	}
	if strings.Contains(rec.Body.String(), "查看全部") {
		t.Error("scoped view must not link to the global pipeline list")
	}
	if !strings.Contains(rec.Body.String(), "返回仓库") {
		t.Error("scoped view missing back-to-repo link")
	}

	// 任务详情与日志 API：非授权用户 403（否则日志泄露）
	if rec = getBrowserAs(t, mux, u, "mallory", "/pipelines/"+t2.ID); rec.Code != 200 {
		t.Errorf("own task detail: status %d", rec.Code)
	}
	if rec = getBrowserAs(t, mux, u, "eve", "/pipelines/"+t2.ID); rec.Code != http.StatusForbidden {
		t.Errorf("foreign task detail: status %d, want 403", rec.Code)
	}
	if rec = getBrowserAs(t, mux, u, "eve", "/api/tasks/"+t2.ID+"/log"); rec.Code != http.StatusForbidden {
		t.Errorf("foreign task log api: status %d, want 403", rec.Code)
	}
	// 已删除仓库的历史：仅管理员可见
	if rec = getBrowserAs(t, mux, u, "eve", "/pipelines/"+t3.ID); rec.Code != http.StatusForbidden {
		t.Errorf("deleted repo task detail: status %d, want 403", rec.Code)
	}
	if rec = getBrowserAs(t, mux, u, "admin", "/pipelines/"+t3.ID); rec.Code != 200 {
		t.Errorf("admin deleted repo task detail: status %d, want 200", rec.Code)
	}

	// 手动部署：Task.Repo 必须是全名 owner/name，并出现在仓库作用域列表里
	req := httptest.NewRequest("POST", "/repos/admin/demo/deploy", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Sessions.New("admin")})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("manual deploy: status %d\n%s", rec.Code, rec.Body.String())
	}
	var manual *model.Task
	for _, ts := range u.Hist.Recent(10) {
		if ts.Repo == "admin/demo" && ts.ID != t1.ID {
			manual = &ts
			break
		}
	}
	if manual == nil {
		t.Fatal("manual deploy task not recorded with full repo name")
	}
	rec = getBrowserAs(t, mux, u, "admin", "/pipelines?repo=admin/demo")
	if !strings.Contains(rec.Body.String(), manual.ID) {
		t.Error("manual deploy task missing from scoped view")
	}
}

// ---- 角色权限矩阵（tester / developer / admin） ----

func TestTesterWebMatrix(t *testing.T) {
	u, mux, _ := newBrowserUI(t)
	if err := u.Store.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatalf("create qa: %v", err)
	}
	// 给测试角色一个已部署过的记录
	task := u.Pipe.NewTask("admin/demo", "main", "a1b2c3d4")
	u.Hist.Add(task)

	// 源码类页面：一律 403
	for _, path := range []string{
		"/repos/admin/demo/tree/main",
		"/repos/admin/demo/tree/main/src",
		"/repos/admin/demo/blob/main/README.md",
		"/repos/admin/demo/raw/main/README.md",
		"/repos/admin/demo/commits/main",
		"/repos/new",
		"/pipelines",
		"/pipelines/" + task.ID,
		"/api/tasks/" + task.ID + "/log",
	} {
		if rec := getBrowserAs(t, mux, u, "qa", path); rec.Code != http.StatusForbidden {
			t.Errorf("tester GET %s: status %d, want 403", path, rec.Code)
		}
	}

	// 触发部署：CanWrite 在 store 层就被 tester 封锁
	req := httptest.NewRequest("POST", "/repos/admin/demo/deploy", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Sessions.New("qa")})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("tester deploy: status %d, want 403", rec.Code)
	}

	// 仓库页：测试视图（部署状态 + 测试入口），无源码元素
	rec = getBrowserAs(t, mux, u, "qa", "/repos/admin/demo")
	if rec.Code != 200 {
		t.Fatalf("tester repo page: status %d\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"最近部署", "测试入口", "main", "a1b2c3d"} {
		if !strings.Contains(body, want) {
			t.Errorf("tester repo page missing %q", want)
		}
	}
	for _, banned := range []string{"Git 地址", "/blob/main/", "提交历史", "重新部署", "README"} {
		if strings.Contains(body, banned) {
			t.Errorf("tester repo page must not contain %q", banned)
		}
	}

	// 仪表盘：测试项目标题，无新建仓库/流水线入口
	rec = getBrowserAs(t, mux, u, "qa", "/")
	if rec.Code != 200 {
		t.Fatalf("tester dashboard: status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "测试项目") {
		t.Error("tester dashboard missing 测试项目 heading")
	}
	// NewTask 初始状态 pending：可读仓库的部署状态对 tester 可见（无流水线链接）
	if !strings.Contains(body, string(model.StatusPending)) {
		t.Error("tester dashboard should show deploy status of readable repos")
	}
	for _, banned := range []string{"新建仓库", "查看流水线", "流水线 →"} {
		if strings.Contains(body, banned) {
			t.Errorf("tester dashboard must not contain %q", banned)
		}
	}

	// /api/status：登录必需，且 tester 不暴露任务历史
	rec = getBrowser(t, mux, "/api/status")
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous /api/status: status %d, want 401", rec.Code)
	}
	rec = getBrowserAs(t, mux, u, "qa", "/api/status")
	if rec.Code != 200 {
		t.Errorf("tester /api/status: status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), task.ID) {
		t.Error("tester /api/status must not include task history")
	}

	// 开发者与管理员的源码/流水线路由不受影响（对照）
	for _, who := range []string{"admin"} {
		for _, path := range []string{"/repos/admin/demo/tree/main", "/pipelines", "/repos/new"} {
			if rec := getBrowserAs(t, mux, u, who, path); rec.Code != 200 {
				t.Errorf("%s GET %s: status %d, want 200", who, path, rec.Code)
			}
		}
	}
}

// ---- 缺陷管理（提交 → 修复 → 复测 → 关闭） ----

// postFormAs 以指定用户身份 POST 表单。
func postFormAs(t *testing.T, mux *http.ServeMux, u *UI, user, target string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Sessions.New(user)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// multipartBug 构造缺陷提交的 multipart 表单。
func multipartBug(t *testing.T, fields map[string]string, shot []byte) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if shot != nil {
		fw, err := w.CreateFormFile("shot0", "bug.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write(shot); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return body, w.FormDataContentType()
}

func TestBugWorkflow(t *testing.T) {
	u, mux, _ := newBrowserUI(t)
	// 组织 team 拥有仓库 app；dev 是组织成员（可被指派），qa 是测试角色
	if err := u.Store.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateUser("dev", "pw-123456", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateOrg("team", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.AddOrgMember("team", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateRepo("app", "org:team", true, ""); err != nil {
		t.Fatal(err)
	}

	// 1. tester 提交缺陷（带截图）
	body, ct := multipartBug(t, map[string]string{
		"title": "登录按钮无效", "severity": "major", "assignee": "dev",
		"steps": "1. 打开登录页\n2. 点击登录", "expected": "进入首页", "actual": "无响应",
	}, []byte("\x89PNG\r\n\x1a\nfake"))
	rec := postFormAs(t, mux, u, "qa", "/repos/team/app/bugs/new", body, ct)
	if rec.Code != http.StatusFound {
		t.Fatalf("bug submit: status %d\n%s", rec.Code, rec.Body.String())
	}
	detailURL := rec.Header().Get("Location")
	detailPath := strings.SplitN(detailURL, "?", 2)[0] // 剥掉 flash 查询串
	bugID := path.Base(detailPath)

	// 详情：tester 视角
	rec = getBrowserAs(t, mux, u, "qa", detailURL)
	if rec.Code != 200 {
		t.Fatalf("bug detail: status %d\n%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"登录按钮无效", "复现步骤", "待修复", "进入首页"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("bug detail missing %q", want)
		}
	}

	// 截图可访问（仓库名空间内）
	rec = getBrowserAs(t, mux, u, "qa", detailPath+"/files/0.png")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") {
		t.Errorf("bug screenshot: status %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	// 2. 开发者标记修复完成（重新部署后）
	rec = postFormAs(t, mux, u, "dev", detailPath+"/status",
		strings.NewReader("to=fixed&note=已修复并重新部署"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("dev mark fixed: status %d\n%s", rec.Code, rec.Body.String())
	}
	bug, _ := u.Store.GetBug(bugID)
	if bug.Status != store.BugFixed {
		t.Fatalf("status = %s, want fixed", bug.Status)
	}

	// 3. 开发者不能关闭（只有报告人/管理员）
	rec = postFormAs(t, mux, u, "dev", detailPath+"/status", strings.NewReader("to=closed"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Errorf("dev close: status %d, want 403", rec.Code)
	}

	// 4. tester 复测不通过 → 重新打开
	rec = postFormAs(t, mux, u, "qa", detailPath+"/status", strings.NewReader("to=open&note=还是点不动"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("qa reopen: status %d", rec.Code)
	}
	bug, _ = u.Store.GetBug(bugID)
	if bug.Status != store.BugOpen {
		t.Fatalf("after reopen status = %s", bug.Status)
	}

	// 5. 开发者再次修复 → tester 复测通过 → 永久关闭
	if rec = postFormAs(t, mux, u, "dev", detailPath+"/status", strings.NewReader("to=fixed"), "application/x-www-form-urlencoded"); rec.Code != http.StatusFound {
		t.Fatalf("dev mark fixed 2: status %d", rec.Code)
	}
	rec = postFormAs(t, mux, u, "qa", detailPath+"/status", strings.NewReader("to=closed"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("qa close: status %d", rec.Code)
	}
	bug, _ = u.Store.GetBug(bugID)
	if bug.Status != store.BugClosed || bug.ClosedAt.IsZero() {
		t.Fatalf("status = %s closedAt zero=%v, want closed", bug.Status, bug.ClosedAt.IsZero())
	}
	if len(bug.Comments) != 4 { // 两次修复 + 一次重开 + 一次关闭的系统记录
		t.Errorf("system comments = %d, want 4", len(bug.Comments))
	}

	// 6. tester 不能标记修复（只有被指派开发/管理员）
	body2, ct2 := multipartBug(t, map[string]string{
		"title": "第二个缺陷", "severity": "minor", "assignee": "dev", "steps": "steps",
	}, nil)
	if rec = postFormAs(t, mux, u, "qa", "/repos/team/app/bugs/new", body2, ct2); rec.Code != http.StatusFound {
		t.Fatalf("bug submit 2: status %d", rec.Code)
	}
	bug2ID := path.Base(strings.SplitN(rec.Header().Get("Location"), "?", 2)[0])
	if rec = postFormAs(t, mux, u, "qa", "/repos/team/app/bugs/"+bug2ID+"/status", strings.NewReader("to=fixed"), "application/x-www-form-urlencoded"); rec.Code != http.StatusForbidden {
		t.Errorf("tester mark fixed: status %d, want 403", rec.Code)
	}

	// 7. tester 不能被指派（候选列表校验）
	body3, ct3 := multipartBug(t, map[string]string{
		"title": "指派给自己", "severity": "minor", "assignee": "qa", "steps": "s",
	}, nil)
	rec = postFormAs(t, mux, u, "qa", "/repos/team/app/bugs/new", body3, ct3)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("self-assign should fail with err redirect, got %d -> %s", rec.Code, rec.Header().Get("Location"))
	}

	// 8. 隔离：缺陷只能属于其仓库
	//    team/app 的缺陷不出现在 admin/demo 的列表里，跨仓库访问详情 404
	rec = getBrowserAs(t, mux, u, "admin", "/repos/admin/demo/bugs")
	if !strings.Contains(rec.Body.String(), "登录按钮无效") == false {
		t.Error("isolation broken: team/app bug visible in admin/demo list")
	}
	rec = getBrowserAs(t, mux, u, "admin", "/repos/admin/demo/bugs/"+bugID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-repo bug detail: status %d, want 404", rec.Code)
	}
	// 截图同样隔离
	rec = getBrowserAs(t, mux, u, "admin", "/repos/admin/demo/bugs/"+bugID+"/files/0.png")
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-repo screenshot: status %d, want 404", rec.Code)
	}

	// 9. 缺陷列表过滤
	rec = getBrowserAs(t, mux, u, "qa", "/repos/team/app/bugs?status=closed")
	if !strings.Contains(rec.Body.String(), "登录按钮无效") || strings.Contains(rec.Body.String(), "第二个缺陷") {
		t.Error("status filter not applied")
	}
}

// ---- 需求管理（创建 → 待开发 → 待核查 → 验收/退回 → 关闭） ----

func TestRequirementFlow(t *testing.T) {
	u, mux, _ := newBrowserUI(t)
	// 组织 team + 开发成员 dev + 测试 qa（tester）
	if err := u.Store.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateUser("dev", "pw-123456", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateOrg("team", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.AddOrgMember("team", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateRepo("app", "org:team", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateUser("outsider", "pw-123456", false, ""); err != nil {
		t.Fatal(err)
	}

	// 1. 测试角色（可读者）创建需求 —— 任何角色都可以提需求
	form := strings.NewReader("title=支持导出PDF&body=%23 背景%0A用户需要导出%0A%0A%23%23 验收标准%0A- 点导出按钮生成PDF")
	rec := postFormAs(t, mux, u, "qa", "/repos/team/app/reqs/new", form, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("req create by tester: status %d\n%s", rec.Code, rec.Body.String())
	}
	detailPath := strings.SplitN(rec.Header().Get("Location"), "?", 2)[0]
	reqID := path.Base(detailPath)

	// 2. 详情渲染 markdown（标题平铺 → 点进去看全部内容）
	rec = getBrowserAs(t, mux, u, "dev", detailPath)
	if rec.Code != 200 {
		t.Fatalf("req detail: status %d", rec.Code)
	}
	for _, want := range []string{"支持导出PDF", "待开发", "验收标准", "创建者", "qa"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("req detail missing %q", want)
		}
	}

	// 3. 非创建者（dev）编辑被拒
	rec = getBrowserAs(t, mux, u, "dev", detailPath+"/edit")
	if rec.Code != http.StatusForbidden {
		t.Errorf("dev edit page: status %d, want 403", rec.Code)
	}
	rec = postFormAs(t, mux, u, "dev", detailPath+"/edit",
		strings.NewReader("title=hacked&body=x"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Errorf("dev edit post: status %d, want 403", rec.Code)
	}

	// 4. 创建者编辑成功
	rec = postFormAs(t, mux, u, "qa", detailPath+"/edit",
		strings.NewReader("title=支持导出PDF（含图表）&body=%0A%23 验收标准%0A- 图表也要导出"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("creator edit: status %d", rec.Code)
	}
	req, _ := u.Store.GetRequirement(reqID)
	if req.Title != "支持导出PDF（含图表）" {
		t.Errorf("edit not applied: %q", req.Title)
	}

	// 5. tester（无写权限）不能标记待核查
	rec = postFormAs(t, mux, u, "qa", detailPath+"/status", strings.NewReader("to=review"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Errorf("tester mark review: status %d, want 403", rec.Code)
	}

	// 6. 组织开发成员（代码提交者）标记待核查
	rec = postFormAs(t, mux, u, "dev", detailPath+"/status", strings.NewReader("to=review&note=功能已上线"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("dev mark review: status %d", rec.Code)
	}
	req, _ = u.Store.GetRequirement(reqID)
	if req.Status != store.ReqReview {
		t.Fatalf("status = %s, want review", req.Status)
	}

	// 7. 非创建者不能关闭
	rec = postFormAs(t, mux, u, "dev", detailPath+"/status", strings.NewReader("to=closed"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Errorf("dev close: status %d, want 403", rec.Code)
	}

	// 8. 创建者核查不通过 → 退回待开发
	rec = postFormAs(t, mux, u, "qa", detailPath+"/status", strings.NewReader("to=pending&note=图表没导出"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("creator reject: status %d", rec.Code)
	}
	req, _ = u.Store.GetRequirement(reqID)
	if req.Status != store.ReqPending {
		t.Fatalf("after reject status = %s", req.Status)
	}

	// 9. dev 再次提交核查 → 创建者验收通过，永久关闭
	if rec = postFormAs(t, mux, u, "dev", detailPath+"/status", strings.NewReader("to=review"), "application/x-www-form-urlencoded"); rec.Code != http.StatusFound {
		t.Fatalf("dev mark review 2: status %d", rec.Code)
	}
	rec = postFormAs(t, mux, u, "qa", detailPath+"/status", strings.NewReader("to=closed"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("creator close: status %d", rec.Code)
	}
	req, _ = u.Store.GetRequirement(reqID)
	if req.Status != store.ReqClosed || req.ClosedAt.IsZero() {
		t.Fatalf("status = %s closedAtZero=%v, want closed", req.Status, req.ClosedAt.IsZero())
	}
	if len(req.Records) != 4 { // review + reject + review + closed
		t.Errorf("records = %d, want 4", len(req.Records))
	}

	// 10. 隔离：需求不出现在其他仓库，跨仓库访问 404
	rec = getBrowserAs(t, mux, u, "admin", "/repos/admin/demo/reqs")
	if strings.Contains(rec.Body.String(), "支持导出PDF") {
		t.Error("isolation broken: team/app req visible in admin/demo list")
	}
	rec = getBrowserAs(t, mux, u, "admin", "/repos/admin/demo/reqs/"+reqID)
	if rec.Code != http.StatusNotFound {
		t.Errorf("cross-repo req detail: status %d, want 404", rec.Code)
	}

	// 11. 同组织任何角色可见：dev 与 tester 都能在列表看到；未登录用户看不到私有需求
	for _, who := range []string{"dev", "qa"} {
		if rec = getBrowserAs(t, mux, u, who, "/repos/team/app/reqs"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "支持导出PDF") {
			t.Errorf("%s req list: status %d", who, rec.Code)
		}
	}

	// 12. 状态过滤
	rec = getBrowserAs(t, mux, u, "qa", "/repos/team/app/reqs?status=closed")
	if !strings.Contains(rec.Body.String(), "支持导出PDF") {
		t.Error("closed filter should include closed req")
	}
}

// ---- SSH 公钥管理 ----

func TestSSHKeysUI(t *testing.T) {
	u, mux, _ := newBrowserUI(t)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubKey, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
	fp := ssh.FingerprintSHA256(pubKey)

	// 添加公钥
	body := strings.NewReader("name=ci&pub=" + urlQueryEscapeForTest(line))
	rec := postFormAs(t, mux, u, "admin", "/me/keys/add", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("key add: status %d\n%s", rec.Code, rec.Body.String())
	}
	// 列表展示指纹与名称
	rec = getBrowserAs(t, mux, u, "admin", "/me/keys")
	if rec.Code != 200 {
		t.Fatalf("keys page: status %d", rec.Code)
	}
	// html/template 会把 "+" 转义为 "&#43;"（UTF-7 防护），比较前先还原
	rendered := strings.ReplaceAll(rec.Body.String(), "&#43;", "+")
	for _, want := range []string{"ci", fp, "ssh-ed25519"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("keys page missing %q", want)
		}
	}
	// 重复指纹被拒
	dup := strings.NewReader("name=dup&pub=" + urlQueryEscapeForTest(line))
	rec = postFormAs(t, mux, u, "admin", "/me/keys/add", dup, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("dup key add: status %d -> %s, want err redirect", rec.Code, rec.Header().Get("Location"))
	}
	// 乱内容被拒
	rec = postFormAs(t, mux, u, "admin", "/me/keys/add",
		strings.NewReader("name=x&pub=not-a-key"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "err=") {
		t.Errorf("invalid key add: status %d -> %s, want err redirect", rec.Code, rec.Header().Get("Location"))
	}
	// 删除
	keys := u.Store.ListSSHKeys("admin")
	if len(keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(keys))
	}
	rec = postFormAs(t, mux, u, "admin", "/me/keys/"+keys[0].ID+"/delete", nil, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("key delete: status %d", rec.Code)
	}
	if len(u.Store.ListSSHKeys("admin")) != 0 {
		t.Error("key not deleted")
	}
	// /me 页含公钥管理入口
	rec = getBrowserAs(t, mux, u, "admin", "/me")
	if !strings.Contains(rec.Body.String(), "/me/keys") {
		t.Error("me page missing keys link")
	}
}

// urlQueryEscapeForTest 表单体编码必须用 QueryEscape（PathEscape 不转义 +）。
func urlQueryEscapeForTest(s string) string {
	return url.QueryEscape(s)
}

// ---- 后端 API 代理（/api/<slug>/ → 127.0.0.1:<port>） ----

func TestAPIProxy(t *testing.T) {
	u, mux, _ := newBrowserUI(t)
	if err := u.Store.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateUser("eve", "pw-123456", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateOrg("team", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.AddOrgMember("team", "qa"); err != nil {
		t.Fatal(err)
	}
	// eve 保持为组织外人员（私有仓库权限负向测试用）
	if err := u.Store.CreateRepo("api-app", "org:team", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateRepo("priv", "org:team", false, ""); err != nil {
		t.Fatal(err)
	}

	// 伪后端：记录收到的路径/方法，回显
	seen := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Method + " " + r.URL.Path
		io.Copy(io.Discard, r.Body)
		w.Header().Set("X-Backend", "yes")
		fmt.Fprintf(w, "backend:%s:%s", r.Method, r.URL.Path)
	}))
	defer backend.Close()
	backendPort := backend.URL[strings.LastIndex(backend.URL, ":")+1:]
	portNum, _ := strconv.Atoi(backendPort)

	// 写入 site meta（port 指向伪后端）
	appDir := u.Pipe.Params.AppDirFor("team/api-app")
	os.MkdirAll(appDir, 0o755)
	os.WriteFile(filepath.Join(appDir, ".gitwo-site.json"),
		[]byte(fmt.Sprintf(`{"port":%d}`, portNum)), 0o644)

	// 1. GET 带子路径：前缀被剥掉，后端收到自身根路径下的请求
	rec := getBrowser(t, mux, "/api/team_api-app/echo/hello")
	if rec.Code != 200 {
		t.Fatalf("proxy GET: status %d\n%s", rec.Code, rec.Body.String())
	}
	if seen != "GET /echo/hello" {
		t.Errorf("backend saw %q, want GET /echo/hello", seen)
	}
	if !strings.Contains(rec.Body.String(), "backend:GET:/echo/hello") {
		t.Errorf("proxy body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Backend") != "yes" {
		t.Error("backend response headers not forwarded")
	}

	// 2. 根路径
	rec = getBrowser(t, mux, "/api/team_api-app")
	if rec.Code != 200 || seen != "GET /" {
		t.Errorf("root proxy: status %d backend saw %q", rec.Code, seen)
	}

	// 3. POST 带体转发
	postBody := strings.NewReader("payload=abc&x=1")
	req := httptest.NewRequest("POST", "/api/team_api-app/echo", postBody)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: u.Sessions.New("admin")})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "POST") {
		t.Errorf("proxy POST: status %d body %q", rec.Code, rec.Body.String())
	}

	// 4. 无后端服务的仓库（纯静态）：提示页 404
	appDir2 := u.Pipe.Params.AppDirFor("team/priv")
	os.MkdirAll(appDir2, 0o755)
	os.WriteFile(filepath.Join(appDir2, ".gitwo-site.json"), []byte(`{"static_dir":"/x"}`), 0o644)
	rec = getBrowserAs(t, mux, u, "qa", "/api/team_priv")
	if rec.Code != http.StatusNotFound {
		t.Errorf("no-port repo: status %d, want 404 message", rec.Code)
	}

	// 5. 未知 slug
	if rec = getBrowser(t, mux, "/api/ghost"); rec.Code != 404 {
		t.Errorf("unknown slug: status %d", rec.Code)
	}

	// 6. 私有仓库权限：eve（非成员，仓库私有）403；组织成员 qa 200
	rec = getBrowser(t, mux, "/api/team_priv")
	if rec.Code != http.StatusFound {
		t.Errorf("anon private api: status %d, want 302 login", rec.Code)
	}
	privDir := u.Pipe.Params.AppDirFor("team/priv")
	os.WriteFile(filepath.Join(privDir, ".gitwo-site.json"),
		[]byte(fmt.Sprintf(`{"port":%d}`, portNum)), 0o644)
	if rec = getBrowserAs(t, mux, u, "eve", "/api/team_priv"); rec.Code != http.StatusForbidden {
		t.Errorf("eve private api: status %d, want 403", rec.Code)
	}
	if rec = getBrowserAs(t, mux, u, "qa", "/api/team_priv"); rec.Code != 200 {
		t.Errorf("org member private api: status %d, want 200", rec.Code)
	}
}

// ---- 部署回滚与我的待办 ----

func TestRollbackRedeploy(t *testing.T) {
	u, mux, bare := newBrowserUI(t)
	if err := u.Store.CreateUser("dev", "pw-123456", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.AddOrgMember("team-org", "dev"); err != nil {
		_ = u.Store.CreateOrg("team-org", "admin")
		_ = u.Store.AddOrgMember("team-org", "dev")
	}
	// 取仓库里第一个提交的 sha（历史版本），须在移动裸仓库目录之前
	out, err := exec.Command("git", "--git-dir", bare, "log", "--format=%H", "--reverse", "refs/heads/main").Output()
	if err != nil {
		t.Fatal(err)
	}
	shas := strings.Fields(strings.TrimSpace(string(out)))
	if len(shas) < 1 {
		t.Fatal("no commits")
	}
	oldSHA := shas[0]

	// 把 demo 归属改为组织（同时移动裸仓库目录），使 dev 有写权限
	if err := u.Store.UpdateRepo("admin/demo", func(m *store.Repo) error {
		m.Owner = "org:team-org"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	newBare := filepath.Join(filepath.Dir(filepath.Dir(bare)), "team-org", "demo.git")
	if err := os.MkdirAll(filepath.Dir(newBare), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(bare, newBare); err != nil {
		t.Fatal(err)
	}

	// 1. 指定历史 sha 重新部署（回滚）
	body := strings.NewReader("sha=" + oldSHA + "&branch=main")
	rec := postFormAs(t, mux, u, "dev", "/repos/team-org/demo/deploy", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound {
		t.Fatalf("rollback deploy: status %d\n%s", rec.Code, rec.Body.String())
	}
	var rollback *model.Task
	for _, ts := range u.Hist.Recent(10) {
		if ts.Repo == "team-org/demo" && ts.SHA == oldSHA {
			rollback = &ts
			break
		}
	}
	if rollback == nil {
		t.Fatal("rollback task not recorded")
	}

	// 2. 不存在的 sha 被拒
	rec = postFormAs(t, mux, u, "dev", "/repos/team-org/demo/deploy",
		strings.NewReader("sha=deadbeefdeadbeef&branch=main"), "application/x-www-form-urlencoded")
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "%E4%B8%8D%E5%AD%98%E5%9C%A8") {
		t.Errorf("bad sha: status %d -> %s, want 不存在 flash", rec.Code, rec.Header().Get("Location"))
	}

	// 3. tester（无写权限）不能回滚
	body = strings.NewReader("sha=" + oldSHA)
	rec = postFormAs(t, mux, u, "qa", "/repos/team-org/demo/deploy", body, "application/x-www-form-urlencoded")
	if rec.Code != http.StatusForbidden {
		t.Errorf("tester rollback: status %d, want 403", rec.Code)
	}
}

func TestDashboardTodos(t *testing.T) {
	u, mux, _ := newBrowserUI(t)
	if err := u.Store.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateUser("dev", "pw-123456", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateOrg("team", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.AddOrgMember("team", "dev"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.AddOrgMember("team", "qa"); err != nil {
		t.Fatal(err)
	}
	if err := u.Store.CreateRepo("app", "org:team", true, ""); err != nil {
		t.Fatal(err)
	}

	// qa 提交缺陷指派给 dev（待修复）
	bug := store.Bug{
		Repo: "team/app", Title: "待修复缺陷", Body: "b", Severity: store.SeverityMajor,
		Status: store.BugOpen, Reporter: "qa", Assignee: "dev", ID: "bug-open-1",
	}
	if err := u.Store.CreateBug(&bug); err != nil {
		t.Fatal(err)
	}
	// dev 标记修复 → 出现在 qa 的"待我复测"，消失于 dev 的"待修复"
	err := u.Store.UpdateBug(bug.ID, func(b *store.Bug) error { b.Status = store.BugFixed; return nil })
	if err != nil {
		t.Fatal(err)
	}
	// qa 创建需求并进入待核查
	req := store.Requirement{
		Repo: "team/app", Title: "待核查需求", Body: "r",
		Status: store.ReqReview, Creator: "qa", ID: "req-review-1",
	}
	if err := u.Store.CreateRequirement(&req); err != nil {
		t.Fatal(err)
	}

	// dev 首页：待修复归零、待复测(qa 的)不出现
	rec := getBrowserAs(t, mux, u, "dev", "/")
	if !strings.Contains(rec.Body.String(), "没有待办事项") {
		t.Errorf("dev todo should be empty after fix (bug belongs to qa verify)")
	}
	// qa 首页：待复测缺陷 + 待核查需求
	rec = getBrowserAs(t, mux, u, "qa", "/")
	for _, want := range []string{"待复测", "待核查", "待修复缺陷", "待核查需求"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("qa todo missing %q", want)
		}
	}
	// 匿名首页无待办区
	rec = getBrowser(t, mux, "/")
	if strings.Contains(rec.Body.String(), "我的待办") {
		t.Error("anonymous dashboard should not show todos")
	}
}
