// Package webui serves the gitwo platform UI: login, repository browser,
// user/group administration, the pipeline (build) views and the /sites/
// routes that expose deployed frontends. It also wires git pushes to the
// deploy queue.
package webui

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitwo/internal/appconfig"
	"gitwo/internal/auth"
	"gitwo/internal/gitserver"
	"gitwo/internal/history"
	"gitwo/internal/model"
	"gitwo/internal/pipeline"
	"gitwo/internal/queue"
	"gitwo/internal/staticserver"
	"gitwo/internal/store"
	"gitwo/internal/version"
)

type UI struct {
	Cfg      *appconfig.Config
	Home     appconfig.Home
	Store    *store.Store
	Sessions *auth.Sessions
	Pipe     *pipeline.Pipeline
	Hist     *history.Store
	Queue    *queue.Queue
	Git      *gitserver.Git
	Log      *slog.Logger

	start time.Time
}

func New(cfg *appconfig.Config, home appconfig.Home, st *store.Store, pipe *pipeline.Pipeline,
	hist *history.Store, q *queue.Queue, log *slog.Logger) *UI {
	u := &UI{
		Cfg: cfg, Home: home, Store: st,
		Sessions: auth.NewSessions(),
		Pipe:     pipe, Hist: hist, Queue: q, Log: log,
		start: time.Now(),
	}
	u.Git = &gitserver.Git{
		ReposDir: home.ReposDir(),
		Store:    st,
		Log:      log,
		OnPush:   u.onPush,
	}
	return u
}

// onPush turns pushed branches into pipeline tasks, honoring the repo's
// deploy branch filter. Same-repo serialization and dedup happen in the queue.
func (u *UI) onPush(repo string, updates []gitserver.RefUpdate) {
	rp, ok := u.Store.GetRepo(repo)
	if !ok {
		return
	}
	for _, up := range updates {
		if up.Deleted {
			u.Log.Info("branch deleted, nothing to deploy", "repo", repo, "branch", up.Branch)
			continue
		}
		if rp.DeployBranch != "" && up.Branch != rp.DeployBranch {
			u.Log.Info("push ignored: not the deploy branch", "repo", repo,
				"branch", up.Branch, "deploy_branch", rp.DeployBranch)
			continue
		}
		task := u.Pipe.NewTask(repo, up.Branch, up.NewSHA)
		u.Hist.Add(task) // register first: Enqueue may start it immediately
		existing, err := u.Queue.Enqueue(task)
		switch {
		case err == nil:
			u.Log.Info("deployment queued from push", "task", task.ID, "repo", repo,
				"branch", up.Branch, "sha", task.ShortSHA())
		case err == queue.ErrDuplicate:
			task.Status = model.StatusSkipped
			task.Error = "duplicate: identical task already queued/running as " + existing.ID
			task.FinishedAt = time.Now()
			u.Hist.Persist(task)
			u.Log.Info("duplicate push collapsed", "repo", repo, "branch", up.Branch, "existing", existing.ID)
		case err == queue.ErrBusy:
			task.Status = model.StatusSkipped
			task.Error = err.Error()
			task.FinishedAt = time.Now()
			u.Hist.Persist(task)
			u.Log.Warn("push skipped: queue full", "repo", repo, "branch", up.Branch)
		default:
			task.Status = model.StatusFailed
			task.Error = err.Error()
			task.FinishedAt = time.Now()
			u.Hist.Persist(task)
			u.Log.Error("enqueue failed", "repo", repo, "err", err)
		}
	}
}

// OnPush 把推送事件交给部署管线（SSH Git 服务与 HTTP 共用同一条链路）。
func (u *UI) OnPush(repo string, updates []gitserver.RefUpdate) {
	u.onPush(repo, updates)
}

func (u *UI) Handler() http.Handler {
	mux := http.NewServeMux()

	// auth
	mux.HandleFunc("GET /login", u.loginPage)
	mux.HandleFunc("POST /login", u.loginPost)
	mux.HandleFunc("POST /logout", u.logoutPost)
	mux.HandleFunc("GET /me", u.mePage)
	mux.HandleFunc("POST /me", u.mePost)
	mux.HandleFunc("GET /me/keys", u.meKeysPage)
	mux.HandleFunc("POST /me/keys/add", u.meKeysAddPost)
	mux.HandleFunc("POST /me/keys/{id}/delete", u.meKeysDeletePost)

	// dashboard
	mux.HandleFunc("GET /{$}", u.dashboard)

	// repos（仓库以 owner/name 标识）
	mux.HandleFunc("GET /repos/new", u.repoNewPage)
	mux.HandleFunc("POST /repos/new", u.repoNewPost)
	mux.HandleFunc("GET /repos/{owner}/{name}", u.repoOverview)
	// 文件浏览：短模式匹配分支根目录（避免 {path...} 为空时的 301 补斜杠）
	mux.HandleFunc("GET /repos/{owner}/{name}/tree/{branch}", u.repoTree)
	mux.HandleFunc("GET /repos/{owner}/{name}/blob/{branch}", u.repoBlob)
	mux.HandleFunc("GET /repos/{owner}/{name}/raw/{branch}", u.repoRaw)
	mux.HandleFunc("GET /repos/{owner}/{name}/tree/{branch}/{path...}", u.repoTree)
	mux.HandleFunc("GET /repos/{owner}/{name}/blob/{branch}/{path...}", u.repoBlob)
	mux.HandleFunc("GET /repos/{owner}/{name}/raw/{branch}/{path...}", u.repoRaw)
	// {branch...}：分支名可含斜杠（feature/pdf），整段作为 ref
	mux.HandleFunc("GET /repos/{owner}/{name}/commits/{branch...}", u.repoCommits)

	// 需求（仓库级迭代需求文档；同组织任何角色可见，内容仅创建者可编辑）
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs", u.repoReqsPage)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs/new", u.reqNewPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/reqs/new", u.reqCreatePost)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs/{id}", u.reqDetail)
	mux.HandleFunc("GET /repos/{owner}/{name}/reqs/{id}/edit", u.reqEditPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/reqs/{id}/edit", u.reqEditPost)
	mux.HandleFunc("POST /repos/{owner}/{name}/reqs/{id}/status", u.reqStatusPost)

	// 缺陷（仓库级隔离：bug 只能提交到其所属仓库）
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs", u.repoBugsPage)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs/new", u.bugNewPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/bugs/new", u.bugNewPost)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs/{id}", u.bugDetail)
	mux.HandleFunc("POST /repos/{owner}/{name}/bugs/{id}/status", u.bugStatusPost)
	mux.HandleFunc("POST /repos/{owner}/{name}/bugs/{id}/comment", u.bugCommentPost)
	mux.HandleFunc("GET /repos/{owner}/{name}/bugs/{id}/files/{file}", u.bugFileServe)
	mux.HandleFunc("GET /repos/{owner}/{name}/settings", u.repoSettingsPage)
	mux.HandleFunc("POST /repos/{owner}/{name}/settings", u.repoSettingsPost)
	mux.HandleFunc("POST /repos/{owner}/{name}/deploy", u.repoDeployPost)

	// admin
	mux.HandleFunc("GET /admin", u.adminPage)
	mux.HandleFunc("GET /admin/users", u.adminUsersPage)
	mux.HandleFunc("POST /admin/users", u.adminUserCreate)
	mux.HandleFunc("POST /admin/users/{name}/role", u.adminUserSetRole)
	mux.HandleFunc("POST /admin/users/{name}/delete", u.adminUserDelete)
	mux.HandleFunc("POST /admin/users/{name}/reset", u.adminUserReset)
	mux.HandleFunc("POST /admin/users/{name}/admin", u.adminUserToggleAdmin)
	mux.HandleFunc("GET /admin/orgs", u.adminOrgsPage)
	mux.HandleFunc("POST /admin/orgs", u.adminOrgCreate)
	mux.HandleFunc("POST /admin/orgs/{name}/delete", u.adminOrgDelete)
	mux.HandleFunc("POST /admin/orgs/{name}/members", u.adminOrgMembers)

	// pipelines
	mux.HandleFunc("GET /pipelines", u.pipelinesPage)
	mux.HandleFunc("GET /pipelines/{id}", u.pipelineDetail)
	mux.HandleFunc("GET /api/tasks/{id}/log", u.taskLogAPI)
	mux.HandleFunc("GET /api/status", u.statusAPI)
	// 后端 API 代理：/api/<slug>/… → 已部署服务（注意：仓库 slug 若取名为
	// "status"/"tasks" 会与上述字面量路由在 GET 上冲突，属已知的边界取舍）
	mux.HandleFunc("/api/{slug}", u.apiProxy)
	mux.HandleFunc("/api/{slug}/{path...}", u.apiProxy)

	// deployed sites
	mux.HandleFunc("GET /sites/{repo}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/sites/"+r.PathValue("repo")+"/", http.StatusFound)
	})
	mux.HandleFunc("GET /sites/{repo}/{path...}", u.siteHandler)

	// git smart HTTP
	u.Git.Mount(mux)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
	// 未匹配路径由 ServeMux 默认返回 404（不要注册 "GET /" 兜底，
	// 它会与 gitserver 的 "/git/" 前缀模式产生路由冲突）。
	return mux
}

// ---- session helpers ----

func (u *UI) sessionUser(r *http.Request) (store.User, bool) {
	name := auth.CurrentUser(r, u.Sessions)
	if name == "" {
		return store.User{}, false
	}
	return u.Store.GetUser(name)
}

// requireLogin redirects anonymous visitors to /login.
func (u *UI) requireLogin(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	usr, ok := u.sessionUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return store.User{}, false
	}
	return usr, true
}

func (u *UI) requireAdmin(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return store.User{}, false
	}
	if !usr.Admin {
		http.Error(w, "需要管理员权限", http.StatusForbidden)
		return store.User{}, false
	}
	return usr, true
}

// parseAnyForm 兼容 urlencoded 与 multipart 两种表单编码（浏览器默认前者，
// 外部脚本/工具常发后者）。解析失败时写出 400 并返回 false。
func parseAnyForm(w http.ResponseWriter, r *http.Request, maxMemory int64) bool {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxMemory); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return false
		}
		return true
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return false
	}
	return true
}

// parseAnyFormSilent 同 parseAnyForm，但出错不写响应（调用方自行决定提示）。
func parseAnyFormSilent(r *http.Request, maxMemory int64) bool {
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		return r.ParseMultipartForm(maxMemory) == nil
	}
	return r.ParseForm() == nil
}

// requireDeveloper 是 requireLogin 加上角色限制：测试角色只能访问测试视图
// （部署状态与站点入口），不能查看源码、克隆仓库或流水线。
func (u *UI) requireDeveloper(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return store.User{}, false
	}
	if usr.Tester() {
		http.Error(w, "测试角色无权访问源码与流水线", http.StatusForbidden)
		return store.User{}, false
	}
	return usr, true
}

// repoAndUser resolves a repo for reading; anonymous users may read public
// repos. Returns ok=false after writing an error/redirect.
func (u *UI) repoForRead(w http.ResponseWriter, r *http.Request) (store.Repo, string, bool) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	rp, ok := u.Store.GetRepo(owner + "/" + name)
	if !ok {
		http.NotFound(w, r)
		return store.Repo{}, "", false
	}
	usr, _ := u.sessionUser(r)
	if !u.Store.CanRead(usr.Name, rp) {
		if usr.Name == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
		} else {
			http.Error(w, "无权访问该仓库", http.StatusForbidden)
		}
		return store.Repo{}, "", false
	}
	return rp, usr.Name, true
}

func (u *UI) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pages.ExecuteTemplate(w, name, data); err != nil {
		u.Log.Error("render page", "template", name, "err", err)
	}
}

func (u *UI) flashRedirect(w http.ResponseWriter, r *http.Request, to, msg string) {
	// one-shot message via query param (internal tool: no cookie flashes)
	sep := "?"
	if strings.Contains(to, "?") {
		sep = "&"
	}
	http.Redirect(w, r, to+sep+"msg="+url.QueryEscape(msg), http.StatusFound)
}

// ---- auth pages ----

func (u *UI) loginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.sessionUser(r); ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	u.render(w, http.StatusOK, "login", loginData{Base: base{Title: "登录"}})
}

func (u *UI) loginPost(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("username"))
	password := r.PostFormValue("password")
	usr, ok := u.Store.VerifyLogin(name, password)
	if !ok {
		time.Sleep(300 * time.Millisecond) // slow down brute force a little
		u.render(w, http.StatusUnauthorized, "login", loginData{
			Base: base{Title: "登录"}, Error: "用户名或密码错误",
			Username: name,
		})
		return
	}
	auth.SetSessionCookie(w, u.Sessions.New(usr.Name))
	u.Log.Info("login", "user", usr.Name, "remote", r.RemoteAddr)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (u *UI) logoutPost(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.SessionCookie); err == nil {
		u.Sessions.Delete(c.Value)
	}
	auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

func (u *UI) mePage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	u.render(w, http.StatusOK, "me", meData{
		Base: u.base(r, "账户设置"),
		User: usr,
		Orgs: u.Store.OrgsOf(usr.Name),
	})
}

func (u *UI) mePost(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	current := r.PostFormValue("current")
	next := r.PostFormValue("password")
	confirm := r.PostFormValue("confirm")
	if _, ok := u.Store.VerifyLogin(usr.Name, current); !ok {
		u.render(w, http.StatusBadRequest, "me", meData{Base: u.base(r, "账户设置"), User: usr,
			Error: "当前密码错误"})
		return
	}
	if next != confirm {
		u.render(w, http.StatusBadRequest, "me", meData{Base: u.base(r, "账户设置"), User: usr,
			Error: "两次输入的新密码不一致"})
		return
	}
	if err := u.Store.SetPassword(usr.Name, next); err != nil {
		u.render(w, http.StatusBadRequest, "me", meData{Base: u.base(r, "账户设置"), User: usr,
			Error: err.Error()})
		return
	}
	u.Log.Info("password changed", "user", usr.Name)
	u.flashRedirect(w, r, "/me", "密码已更新")
}

// ---- dashboard ----

type repoRow struct {
	store.Repo
	SiteURL  string
	HasSite  bool
	LastTask *model.Task
}

func (u *UI) dashboard(w http.ResponseWriter, r *http.Request) {
	usr, _ := u.sessionUser(r)
	var rows []repoRow
	latest := map[string]*model.Task{}
	for _, t := range u.Hist.Recent(100) {
		if _, ok := latest[t.Repo]; !ok {
			tt := t
			latest[t.Repo] = &tt
		}
	}
	for _, rp := range u.Store.VisibleRepos(usr.Name) {
		row := repoRow{Repo: rp, LastTask: latest[rp.FullName()]}
		if meta, ok := pipeline.SiteMeta(u.Pipe.Params.AppDirFor(rp.FullName())); ok {
			if meta.StaticDir != "" {
				row.HasSite = true
				row.SiteURL = "/sites/" + rp.Slug() + "/"
			}
		}
		rows = append(rows, row)
	}
	data := dashData{
		Base:     u.base(r, "仓库"),
		Repos:    rows,
		LoggedIn: usr.Name != "",
		Admin:    usr.Admin,
		Tester:   usr.Tester(),
	}
	if usr.Name != "" {
		// 我的待办：指派给我的待修复缺陷 / 我提交的待复测缺陷 / 待我核查的需求
		for _, b := range u.Store.ListBugs("") {
			switch {
			case b.Status == store.BugOpen && b.Assignee == usr.Name:
				data.TodoBugsFix = append(data.TodoBugsFix, b)
			case b.Status == store.BugFixed && b.Reporter == usr.Name:
				data.TodoBugsTest = append(data.TodoBugsTest, b)
			}
			if len(data.TodoBugsFix)+len(data.TodoBugsTest) >= 10 {
				break
			}
		}
		for _, q := range u.Store.ListRequirements("") {
			if q.Status == store.ReqReview && q.Creator == usr.Name {
				data.TodoReqsCheck = append(data.TodoReqsCheck, q)
				if len(data.TodoReqsCheck) >= 10 {
					break
				}
			}
		}
	}
	u.render(w, http.StatusOK, "dash", data)
}

func (u *UI) base(r *http.Request, title string) base {
	usr, _ := u.sessionUser(r)
	return base{
		Title:  title,
		User:   usr.Name,
		Admin:  usr.Admin,
		Tester: usr.Tester(),
		Msg:    r.URL.Query().Get("msg"),
		Host:   r.Host, // host[:port], used to build git/site URLs
	}
}

// ---- deployed sites ----

// siteHandler serves deployed frontends: static dirs via the built-in file
// server, running services via a path-rewriting reverse proxy.
func (u *UI) siteHandler(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("repo")
	rp, ok := u.Store.GetRepoBySlug(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	usr, _ := u.sessionUser(r)
	if !u.Store.CanRead(usr.Name, rp) {
		if usr.Name == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
		} else {
			http.Error(w, "无权访问该站点", http.StatusForbidden)
		}
		return
	}

	meta, ok := pipeline.SiteMeta(u.Pipe.Params.AppDirFor(rp.FullName()))
	if !ok {
		u.render(w, http.StatusNotFound, "message", messageData{
			Base:  u.base(r, "站点未部署"),
			Title: "站点尚未部署",
			Body:  "仓库 " + rp.FullName() + " 还没有成功部署过。推送一个包含 deploy.yaml 的提交后会自动构建并出现在这里。",
		})
		return
	}

	prefix := "/sites/" + slug
	if meta.StaticDir != "" {
		spa := meta.SPA == nil || *meta.SPA // default: SPA fallback on
		h := http.StripPrefix(prefix, staticserver.Handler(meta.StaticDir, spa))
		h.ServeHTTP(w, r)
		return
	}
	if u.proxyBackend(w, r, prefix, meta) {
		return
	}
	u.render(w, http.StatusNotFound, "message", messageData{
		Base:  u.base(r, "站点未就绪"),
		Title: "站点没有可访问内容",
		Body:  "仓库 " + rp.FullName() + " 已部署，但既没有前端静态目录，也没有可代理的服务端口。",
	})
}

// proxyBackend 把请求反代到该仓库已部署的后端服务（127.0.0.1:<port>），
// 并剥掉路径前缀（后端看到的是自身根路径下的请求）。
// 返回 false 表示没有可代理的端口（纯静态仓库）。
func (u *UI) proxyBackend(w http.ResponseWriter, r *http.Request, prefix string, meta pipeline.SiteInfo) bool {
	if meta.Port <= 0 {
		return false
	}
	target := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(meta.Port)}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, prefix)
			if pr.Out.URL.Path == "" || pr.Out.URL.Path == prefix {
				pr.Out.URL.Path = "/"
			}
			pr.Out.Host = pr.In.Host
		},
	}
	proxy.ServeHTTP(w, r)
	return true
}

// apiProxy 后端 API 代理：/api/<slug>/… → 该仓库已部署的服务。
// 前端 /sites/<slug>/ 与后端 /api/<slug>/ 共用 9527 一个端口，
// 后端程序无需单独暴露端口或开防火墙。全 HTTP 方法可用。
func (u *UI) apiProxy(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	rp, ok := u.Store.GetRepoBySlug(slug)
	if !ok {
		http.NotFound(w, r)
		return
	}
	usr, _ := u.sessionUser(r)
	if !u.Store.CanRead(usr.Name, rp) {
		if usr.Name == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
		} else {
			http.Error(w, "无权访问该服务的 API", http.StatusForbidden)
		}
		return
	}
	meta, ok := pipeline.SiteMeta(u.Pipe.Params.AppDirFor(rp.FullName()))
	if !ok || meta.Port <= 0 {
		u.render(w, http.StatusNotFound, "message", messageData{
			Base:  u.base(r, "API 未部署"),
			Title: "该仓库没有已部署的后端服务",
			Body:  `API 代理仅适用于 type: service / dual 且已成功部署的仓库；纯静态站点请访问 /sites/` + slug + `/。`,
		})
		return
	}
	u.proxyBackend(w, r, "/api/"+slug, meta)
}

// ---- status api ----

type statusResponse struct {
	Service   string       `json:"service"`
	Version   string       `json:"version"`
	Uptime    string       `json:"uptime"`
	Listen    string       `json:"listen"`
	Workers   int          `json:"workers"`
	Pending   int          `json:"queue_pending"`
	Running   int          `json:"running"`
	RepoCount int          `json:"repos"`
	Recent    []model.Task `json:"recent"`
}

func (u *UI) statusAPI(w http.ResponseWriter, r *http.Request) {
	usr, _ := u.sessionUser(r)
	if usr.Name == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "login required"})
		return
	}
	resp := statusResponse{
		Service:   "gitwo",
		Version:   version.Version,
		Uptime:    time.Since(u.start).Round(time.Second).String(),
		Listen:    u.Cfg.Listen,
		Workers:   u.Queue.Workers(),
		Pending:   u.Queue.Pending(),
		Running:   u.Hist.Count(model.StatusRunning),
		RepoCount: len(u.Store.ListRepos()),
	}
	if !usr.Tester() {
		// 测试角色不暴露任务历史（含分支/提交/错误输出）
		resp.Recent = u.Hist.Recent(20)
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
