// Package gitserver implements the git smart HTTP protocol (clone/fetch via
// git-upload-pack, push via git-receive-pack) by shelling out to the system
// git in --stateless-rpc mode. Pushes are detected by diffing branch refs
// before/after receive-pack and reported through OnPush, which drives the
// auto-deploy pipeline.
package gitserver

import (
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gitwo/internal/store"
)

var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// RefUpdate is one branch change observed after a push.
type RefUpdate struct {
	Branch  string
	NewSHA  string
	Deleted bool
}

type Git struct {
	ReposDir string
	Store    *store.Store
	Log      *slog.Logger
	OnPush   func(repo string, updates []RefUpdate)
	SelfExe  string // gitwo 自身路径（写入 pre-receive 钩子）
	DataDir  string // 数据目录（钩子子命令经 GITWO_HOME 加载配置）

	locks sync.Map // repo name -> *sync.Mutex (serialize pushes per repo)
}

// Mount registers the /git/ routes on mux.
func (g *Git) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/git/", g.serveGit)
}

// serveGit dispatches the smart HTTP protocol under /git/<owner>/<name>.git:
//
//	<owner>/<name>/info/refs            (GET)
//	<owner>/<name>/git-upload-pack      (POST)
//	<owner>/<name>/git-receive-pack     (POST)
func (g *Git) serveGit(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/git/")
	rest = strings.TrimSuffix(rest, "/")

	// action: "info/refs" is two segments, the pack services are one.
	var repoFull, action string
	switch {
	case strings.HasSuffix(rest, "/info/refs"):
		repoFull, action = strings.TrimSuffix(rest, "/info/refs"), "info/refs"
	default:
		i := strings.LastIndex(rest, "/")
		if i < 0 {
			http.NotFound(w, r)
			return
		}
		repoFull, action = rest[:i], rest[i+1:]
		if action != "git-upload-pack" && action != "git-receive-pack" {
			http.NotFound(w, r)
			return
		}
	}

	full := strings.TrimSuffix(repoFull, ".git")
	if _, _, ok := store.SplitFullName(full); !ok {
		http.NotFound(w, r)
		return
	}
	repo := full
	if _, ok := g.Store.GetRepo(repo); !ok {
		http.NotFound(w, r)
		return
	}

	switch {
	case action == "info/refs" && r.Method == http.MethodGet:
		g.infoRefs(w, r, repo)
	case action == "git-upload-pack" && r.Method == http.MethodPost:
		g.servicePack(w, r, repo, "git-upload-pack")
	case action == "git-receive-pack" && r.Method == http.MethodPost:
		g.servicePack(w, r, repo, "git-receive-pack")
	default:
		http.NotFound(w, r)
	}
}

func (g *Git) repoPath(repo string) string {
	owner, name, ok := store.SplitFullName(repo)
	if !ok {
		owner, name = "legacy", repo
	}
	return filepath.Join(g.ReposDir, owner, name+".git")
}

// basicAuth resolves the requesting user; returns (user, ok). When ok is
// false with needAuth it responds 401 so git clients prompt for credentials.
func (g *Git) auth(w http.ResponseWriter, r *http.Request, repo string, needWrite bool) (string, bool) {
	rp, _ := g.Store.GetRepo(repo)
	needAuth := !rp.Public || needWrite
	user, pass, hasBasic := r.BasicAuth()

	var userName string
	if hasBasic {
		u, ok := g.Store.VerifyLogin(user, pass)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="gitwo"`)
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return "", false
		}
		if u.Tester() {
			// 测试角色无 git 访问权：clone 读源码、push 触发部署都不允许
			http.Error(w, "forbidden: tester role has no git access", http.StatusForbidden)
			return "", false
		}
		userName = u.Name
	}

	if needWrite {
		if !g.Store.CanWrite(userName, rp) {
			if !hasBasic {
				w.Header().Set("WWW-Authenticate", `Basic realm="gitwo"`)
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return "", false
			}
			http.Error(w, "forbidden: no write access to "+repo, http.StatusForbidden)
			return "", false
		}
		return userName, true
	}
	// read access
	if !needAuth {
		return "", true // anonymous read of a public repo
	}
	if g.Store.CanRead(userName, rp) {
		return userName, true
	}
	if !hasBasic {
		w.Header().Set("WWW-Authenticate", `Basic realm="gitwo"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return "", false
	}
	http.Error(w, "forbidden: no read access to "+repo, http.StatusForbidden)
	return "", false
}

func isSmartService(s string) bool {
	return s == "git-upload-pack" || s == "git-receive-pack"
}

func (g *Git) infoRefs(w http.ResponseWriter, r *http.Request, repo string) {
	service := r.URL.Query().Get("service")
	if !isSmartService(service) {
		http.Error(w, "smart HTTP protocol required (git >= 1.6.6)", http.StatusBadRequest)
		return
	}
	if _, ok := g.auth(w, r, repo, service == "git-receive-pack"); !ok {
		return
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-advertisement")
	w.Header().Set("Cache-Control", "no-cache")
	pkt := fmt.Sprintf("# service=%s\n", service)
	fmt.Fprintf(w, "%04x%s0000", len(pkt)+4, pkt)

	cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), "--stateless-rpc", "--advertise-refs", g.repoPath(repo))
	cmd.Env = gitEnv(r)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		g.Log.Error("git advertise-refs failed", "repo", repo, "err", err)
		return
	}
	w.Write(out)
}

func gitEnv(r *http.Request) []string {
	// 继承完整环境：pre-receive 钩子子进程需要 PATH/sh 与 GITWO_* 变量
	env := append(os.Environ(), "GIT_HTTP_EXPORT_ALL=1")
	if p := r.Header.Get("Git-Protocol"); p != "" {
		env = append(env, "GIT_PROTOCOL="+p)
	}
	return env
}

// servicePack handles both stateless-rpc services: stream the request body
// into git and stream git's output back to the client.
func (g *Git) servicePack(w http.ResponseWriter, r *http.Request, repo, service string) {
	needWrite := service == "git-receive-pack"
	user, ok := g.auth(w, r, repo, needWrite)
	if !ok {
		return
	}

	body, err := maybeGzip(r.Body, r.Header.Get("Content-Encoding"))
	if err != nil {
		http.Error(w, "bad request body", http.StatusBadRequest)
		return
	}

	repoPath := g.repoPath(repo)
	var before map[string]string
	var unlock func()
	if needWrite {
		mu := g.lockFor(repo)
		mu.Lock()
		unlock = mu.Unlock
		defer unlock()
		if before, err = gitRefs(repoPath); err != nil {
			g.Log.Error("snapshot refs before push", "repo", repo, "err", err)
		}
	}

	w.Header().Set("Content-Type", "application/x-"+service+"-result")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	cmd := exec.Command("git", strings.TrimPrefix(service, "git-"), "--stateless-rpc", repoPath)
	cmd.Env = append(gitEnv(r), g.pushIdentityEnv(user, repo)...)
	cmd.Stdin = body
	cmd.Stdout = flushWriter{w}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		// Headers/body may be partially sent; the git client surfaces the
		// remote's own error message from the sideband channel.
		g.Log.Error("git service failed", "service", service, "repo", repo,
			"err", runErr, "stderr", stderr.String())
		return
	}

	if needWrite && unlock != nil {
		after, err := gitRefs(repoPath)
		if err != nil {
			g.Log.Error("snapshot refs after push", "repo", repo, "err", err)
			return
		}
		if updates := diffRefs(before, after); len(updates) > 0 && g.OnPush != nil {
			g.OnPush(repo, updates)
		}
	}
}

func (g *Git) lockFor(repo string) *sync.Mutex {
	v, _ := g.locks.LoadOrStore(repo, &sync.Mutex{})
	return v.(*sync.Mutex)
}

func maybeGzip(r io.Reader, encoding string) (io.Reader, error) {
	if strings.Contains(strings.ToLower(encoding), "gzip") {
		return gzip.NewReader(r)
	}
	return r, nil
}

type flushWriter struct{ w http.ResponseWriter }

func (f flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}

// gitRefs returns branch name -> sha of a bare repo.
// RefSnapshot 返回仓库当前分支→SHA 快照（导出供 SSH 服务复用推送检测）。
func RefSnapshot(repoPath string) (map[string]string, error) {
	return gitRefs(repoPath)
}

// DiffRefs 对比前后快照得到分支更新（新增/变更/删除）。
func DiffRefs(before, after map[string]string) []RefUpdate {
	return diffRefs(before, after)
}

func gitRefs(repoPath string) (map[string]string, error) {
	cmd := exec.Command("git", "for-each-ref", "--format=%(refname:short)%00%(objectname)", "refs/heads")
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		name, sha, ok := strings.Cut(line, "\x00")
		if ok {
			refs[name] = sha
		}
	}
	return refs, nil
}

// diffRefs returns added/changed branches (deletions marked Deleted).
func diffRefs(before, after map[string]string) []RefUpdate {
	var ups []RefUpdate
	for name, sha := range after {
		if before[name] != sha {
			ups = append(ups, RefUpdate{Branch: name, NewSHA: sha})
		}
	}
	for name := range before {
		if _, ok := after[name]; !ok {
			ups = append(ups, RefUpdate{Branch: name, Deleted: true})
		}
	}
	return ups
}
