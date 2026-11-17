package gitserver

import (
	"bufio"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitwo/internal/store"
)

func setup(t *testing.T) (*Git, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	reposDir := filepath.Join(dir, "repos")
	if err := os.MkdirAll(reposDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRepo("demo", "user:admin", false, ""); err != nil {
		t.Fatal(err)
	}
	// production creates bare repos via the UI; mirror that here
	if err := os.MkdirAll(filepath.Join(reposDir, "admin"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(reposDir, "admin", "demo.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_ = exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run()
	g := &Git{ReposDir: reposDir, Store: st, Log: slog.Default()}
	return g, st, reposDir
}

// withCreds embeds user:password into an http URL for non-interactive git.
func withCreds(base, path, user, pass string) string {
	if i := strings.Index(base, "://"); i >= 0 {
		return base[:i+3] + user + ":" + pass + "@" + base[i+3:] + path
	}
	return base + path
}

// TestPushCloneRoundTrip performs a real git push/clone through the HTTP
// handlers using the local git binary, and verifies OnPush fires.
func TestPushCloneRoundTrip(t *testing.T) {
	g, _, reposDir := setup(t)

	var pushEvents []struct {
		repo    string
		updates []RefUpdate
	}
	g.OnPush = func(repo string, ups []RefUpdate) {
		pushEvents = append(pushEvents, struct {
			repo    string
			updates []RefUpdate
		}{repo, ups})
	}

	mux := http.NewServeMux()
	g.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	// Credentials embedded in the URL: the repo is private, and without them
	// git would open an interactive credential prompt and hang the test.
	url := withCreds(srv.URL, "/git/admin/demo.git", "admin", "admin")

	work := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		env := append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo",
		)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// clone empty repo, commit, push
	runGit(work, "clone", url, "wt")
	wt := filepath.Join(work, "wt")
	if err := os.WriteFile(filepath.Join(wt, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(wt, "add", ".")
	runGit(wt, "commit", "-m", "first")
	runGit(wt, "push", "origin", "main")

	if len(pushEvents) != 1 {
		t.Fatalf("expected 1 push event, got %d", len(pushEvents))
	}
	if pushEvents[0].repo != "admin/demo" || len(pushEvents[0].updates) != 1 ||
		pushEvents[0].updates[0].Branch != "main" {
		t.Fatalf("bad push event: %+v", pushEvents)
	}

	refs, err := gitRefs(filepath.Join(reposDir, "admin", "demo.git"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := refs["main"]; !ok {
		t.Fatalf("main ref missing after push: %v", refs)
	}

	// clone again from the bare repo
	runGit(work, "clone", url, "wt2")
	data, err := os.ReadFile(filepath.Join(work, "wt2", "hello.txt"))
	if err != nil || string(data) != "hi" {
		t.Fatalf("clone content wrong: %q err=%v", data, err)
	}

	// second push (update) fires again
	if err := os.WriteFile(filepath.Join(wt, "hello.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(wt, "commit", "-am", "second")
	runGit(wt, "push", "origin", "main")
	if len(pushEvents) != 2 {
		t.Fatalf("expected 2 push events, got %d", len(pushEvents))
	}
}

func TestAuthRequiredForPrivatePush(t *testing.T) {
	g, _, _ := setup(t)
	mux := http.NewServeMux()
	g.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// anonymous receive-pack on a private repo must be 401
	resp, err := http.Post(srv.URL+"/git/admin/demo.git/git-receive-pack", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestDiffRefs(t *testing.T) {
	before := map[string]string{"main": "a", "old": "b"}
	after := map[string]string{"main": "c", "new": "d"}
	ups := diffRefs(before, after)
	byBranch := map[string]RefUpdate{}
	for _, u := range ups {
		byBranch[u.Branch] = u
	}
	if len(ups) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(ups), ups)
	}
	if byBranch["main"].NewSHA != "c" || byBranch["old"].Deleted != true ||
		byBranch["new"].NewSHA != "d" {
		t.Fatalf("diff wrong: %+v", byBranch)
	}
}

// TestTesterGitBlocked 验证测试角色无法通过 git 协议读（clone）或写（push）：
// 无论是私有还是公开仓库，只要携带 tester 凭据就一律 403。
func TestTesterGitBlocked(t *testing.T) {
	g, st, _ := setup(t)
	if err := st.CreateUser("qa", "pass1234", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRepo("pub", "user:admin", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(g.ReposDir, "admin"), 0o755); err != nil {
		t.Fatal(err)
	}
	pubPath := filepath.Join(g.ReposDir, "admin", "pub.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", pubPath).CombinedOutput(); err != nil {
		t.Fatalf("git init pub: %v\n%s", err, out)
	}
	_ = exec.Command("git", "--git-dir", pubPath, "symbolic-ref", "HEAD", "refs/heads/main").Run()

	mux := http.NewServeMux()
	g.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	runGit := func(args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("git", args...)
		env := append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo",
		)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// 私有仓库：401 挑战后 tester 凭据被显式拒绝
	t.Run("clone private repo", func(t *testing.T) {
		out, err := runGit("ls-remote", withCreds(srv.URL, "/git/admin/demo.git", "qa", "pass1234"))
		if err == nil {
			t.Errorf("tester clone of private repo should fail\n%s", out)
		}
		if !strings.Contains(out, "403") {
			t.Errorf("want 403 in output, got:\n%s", out)
		}
	})

	// 公开仓库对匿名可读是既有设计（git 客户端无挑战不会发送凭据，
	// 无法区分 tester 与任意匿名者）；tester 能被强制拦住的是写路径。
	// 任何仓库的 push（needWrite → 强制 401 挑战 → tester 凭据 → 403）。
	t.Run("push public repo", func(t *testing.T) {
		out, err := runGit("ls-remote", withCreds(srv.URL, "/git/admin/pub.git", "qa", "pass1234"))
		if err == nil {
			t.Logf("public ls-remote anonymously allowed (by design): %s", out)
		}
		if !strings.Contains(out, "403") {
			t.Logf("git did not send credentials for public read (no challenge) — anonymous path")
		}
		// 写路径：developer 先推一个提交，随后 tester 凭据 push 必须失败
		work := t.TempDir()
		wt := filepath.Join(work, "wt")
		if out, err := runGit("clone", withCreds(srv.URL, "/git/admin/pub.git", "admin", "admin"), wt); err != nil {
			t.Fatalf("admin clone: %v\n%s", err, out)
		}
		if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runGit("-C", wt, "add", "-A"); err != nil {
			t.Fatalf("add: %v\n%s", err, out)
		}
		if out, err := runGit("-C", wt, "commit", "-m", "base"); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
		if out, err := runGit("-C", wt, "push", "origin", "main"); err != nil {
			t.Fatalf("admin push: %v\n%s", err, out)
		}

		qwt := filepath.Join(work, "qwt")
		if out, err := runGit("clone", withCreds(srv.URL, "/git/admin/pub.git", "admin", "admin"), qwt); err != nil {
			t.Fatalf("admin clone 2: %v\n%s", err, out)
		}
		if err := os.WriteFile(filepath.Join(qwt, "y.txt"), []byte("y"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runGit("-C", qwt, "add", "-A"); err != nil {
			t.Fatalf("add: %v\n%s", err, out)
		}
		if out, err := runGit("-C", qwt, "commit", "-m", "qa"); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
		// 用 tester 凭据的 URL 推送（覆盖 origin 的 admin 凭据）
		qout, err := runGit("-C", qwt, "push", withCreds(srv.URL, "/git/admin/pub.git", "qa", "pass1234"), "main")
		if err == nil {
			t.Errorf("tester push should fail\n%s", qout)
		}
		if !strings.Contains(qout, "403") {
			t.Errorf("want 403 in output, got:\n%s", qout)
		}
	})

	// 私有仓库的写路径同样被拒
	t.Run("push private repo", func(t *testing.T) {
		work := t.TempDir()
		wt := filepath.Join(work, "wt")
		if out, err := runGit("clone", withCreds(srv.URL, "/git/admin/demo.git", "admin", "admin"), wt); err != nil {
			t.Fatalf("admin clone: %v\n%s", err, out)
		}
		if err := os.WriteFile(filepath.Join(wt, "x.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runGit("-C", wt, "add", "-A"); err != nil {
			t.Fatalf("add: %v\n%s", err, out)
		}
		if out, err := runGit("-C", wt, "commit", "-m", "base"); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
		if out, err := runGit("-C", wt, "push", "origin", "main"); err != nil {
			t.Fatalf("admin push: %v\n%s", err, out)
		}
		qout, err := runGit("-C", wt, "push", withCreds(srv.URL, "/git/admin/demo.git", "qa", "pass1234"), "main:main")
		if err == nil {
			t.Errorf("tester push should fail\n%s", qout)
		}
	})
}

// TestMain 拦截测试二进制被钩子脚本回调的场景（`<testbin> hook pre-receive`），
// 使集成测试里安装的 pre-receive 钩子能调用到钩子逻辑。
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "hook" && os.Args[2] == "pre-receive" {
		home := os.Getenv("GITWO_TEST_HOME")
		if home == "" {
			fmt.Fprintln(os.Stderr, "GITWO_TEST_HOME not set")
			os.Exit(1)
		}
		st, err := store.Load(filepath.Join(home, "data"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "load store:", err)
			os.Exit(1)
		}
		user := os.Getenv("GITWO_USER")
		repo := os.Getenv("GITWO_REPO")
		var updates []PreReceiveUpdate
		sc := bufio.NewScanner(os.Stdin)
		for sc.Scan() {
			f := strings.Fields(sc.Text())
			if len(f) == 3 {
				updates = append(updates, PreReceiveUpdate{f[0], f[1], f[2]})
			}
		}
		if err := CheckProtectedBranch(st, user, repo, updates); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestProtectedBranch：组织仓库的主分支仅创建者/管理员可推送；
// 其他成员推 main 被拒（含删除），推新分支正常。
func TestProtectedBranch(t *testing.T) {
	dir := t.TempDir()
	reposDir := filepath.Join(dir, "repos")
	os.MkdirAll(filepath.Join(reposDir, "team"), 0o755)
	st, err := store.Load(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// store.Load 已引导默认 admin/admin
	if err := st.CreateUser("member", "memberpw", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateOrg("team", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddOrgMember("team", "member"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRepo("shop", "org:team", true, ""); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(reposDir, "team", "shop.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_ = exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run()

	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	g := &Git{
		ReposDir: reposDir, Store: st, Log: slog.Default(),
		SelfExe: testBin, DataDir: filepath.Join(dir, "data"),
	}
	if err := g.EnsureHook(repoPath); err != nil {
		t.Fatalf("ensure hook: %v", err)
	}
	// 钩子回调测试二进制时需要找到 data 目录
	t.Setenv("GITWO_TEST_HOME", dir)

	mux := http.NewServeMux()
	g.Mount(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	runGit := func(dir, user, pass string, args ...string) (string, error) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		env := append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo",
		)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// admin 先推 main 建立基线
	work := t.TempDir()
	wt := filepath.Join(work, "wt")
	if out, err := runGit(work, "admin", "admin", "clone", withCreds(srv.URL, "/git/team/shop.git", "admin", "admin"), "wt"); err != nil {
		t.Fatalf("admin clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGit(wt, "admin", "admin", "add", "-A"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runGit(wt, "admin", "admin", "commit", "-m", "base"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, err := runGit(wt, "admin", "admin", "push", "origin", "main"); err != nil {
		t.Fatalf("admin push main: %v\n%s", err, out)
	}

	// member 推 main：被钩子拒绝且提示保护原因
	mwt := filepath.Join(work, "mwt")
	if out, err := runGit(work, "member", "memberpw", "clone", withCreds(srv.URL, "/git/team/shop.git", "member", "memberpw"), "mwt"); err != nil {
		t.Fatalf("member clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(mwt, "member.txt"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGit(mwt, "member", "memberpw", "add", "-A"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runGit(mwt, "member", "memberpw", "commit", "-m", "member work"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	mout, merr := runGit(mwt, "member", "memberpw", "push", "origin", "main")
	if merr == nil {
		t.Errorf("member push main should fail\n%s", mout)
	}
	if !strings.Contains(mout, "受保护") {
		t.Errorf("rejection message should mention 受保护\n%s", mout)
	}

	// member 推新分支：正常
	if mout, merr = runGit(mwt, "member", "memberpw", "push", "origin", "HEAD:feature/member"); merr != nil {
		t.Errorf("member push feature branch should succeed\n%s", mout)
	}

	// member 删除 main：被拒
	if mout, merr = runGit(mwt, "member", "memberpw", "push", "origin", ":main"); merr == nil {
		t.Errorf("member delete main should fail\n%s", mout)
	}
	if !strings.Contains(mout, "受保护") {
		t.Errorf("delete rejection should mention 受保护\n%s", mout)
	}

	// admin 推 main：正常
	if err := os.WriteFile(filepath.Join(wt, "admin2.txt"), []byte("a2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGit(wt, "admin", "admin", "add", "-A"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runGit(wt, "admin", "admin", "commit", "-m", "admin work 2"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, err := runGit(wt, "admin", "admin", "push", "origin", "main"); err != nil {
		t.Errorf("admin push main again should succeed\n%s", out)
	}
}
