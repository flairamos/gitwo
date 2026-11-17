package webui

import (
	"bytes"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitwo/internal/model"
	"gitwo/internal/pipeline"
	"gitwo/internal/store"
)

var repoNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// refRE approximates the safe git ref syntax: no whitespace/control chars,
// no shell metacharacters. Values from URLs are passed as git argv, and a
// value starting with "-" would be parsed as a git option (e.g. an injected
// `--output=/path` in `git log`), so leading dashes and ".." are rejected.
var refRE = regexp.MustCompile(`^[A-Za-z0-9._/~^}{:=+-]+$`)

// safeRef validates a branch/ref value coming from a URL.
func safeRef(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
		return false
	}
	return refRE.MatchString(s)
}

// safePath validates a file path inside a tree: no traversal, no option
// injection, no control characters or shell metacharacters.
func safePath(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.Contains(s, "..") {
		return false
	}
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	for _, bad := range []string{";", "|", "&", "$", "`", "'", `"`, "<", ">", "\\"} {
		if strings.Contains(s, bad) {
			return false
		}
	}
	return true
}

// ---- repo creation ----

type ownerOption struct {
	Value string // "user:alice" or "group:devs"
	Label string
}

func (u *UI) ownerOptions(user store.User) []ownerOption {
	var opts []ownerOption
	opts = append(opts, ownerOption{Value: "user:" + user.Name, Label: "我（" + user.Name + "）"})
	for _, o := range u.Store.ListOrgs() {
		if user.Admin || u.Store.IsOrgMember(o.Name, user.Name) {
			opts = append(opts, ownerOption{Value: "org:" + o.Name, Label: "组织 " + o.Name})
		}
	}
	if user.Admin {
		for _, other := range u.Store.ListUsers() {
			if other.Name != user.Name {
				opts = append(opts, ownerOption{Value: "user:" + other.Name, Label: "用户 " + other.Name})
			}
		}
	}
	return opts
}

func (u *UI) repoNewPage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return
	}
	u.render(w, http.StatusOK, "repo_new", repoNewData{
		Base:   u.base(r, "新建仓库"),
		Owners: u.ownerOptions(usr),
	})
}

func (u *UI) repoNewPost(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	owner := r.PostFormValue("owner")
	public := r.PostFormValue("visibility") == "public"
	deployBranch := strings.TrimSpace(r.PostFormValue("deploy_branch"))

	// only admins may create for others
	if !usr.Admin && owner != "user:"+usr.Name &&
		!u.Store.IsOrgMember(strings.TrimPrefix(owner, "org:"), usr.Name) {
		http.Error(w, "只能为自己或所在组织创建仓库", http.StatusForbidden)
		return
	}
	if err := u.Store.CreateRepo(name, owner, public, deployBranch); err != nil {
		u.render(w, http.StatusBadRequest, "repo_new", repoNewData{
			Base: u.base(r, "新建仓库"), Owners: u.ownerOptions(usr), Error: err.Error(), Name: name,
		})
		return
	}
	ownerName := strings.TrimPrefix(owner, "user:")
	ownerName = strings.TrimPrefix(ownerName, "org:")
	full := ownerName + "/" + name
	repoPath := filepath.Join(u.Home.ReposDir(), ownerName, name+".git")
	if err := exec.Command("git", "init", "--bare", "--quiet", repoPath).Run(); err != nil {
		u.Store.DeleteRepo(full)
		os.RemoveAll(repoPath) // 清理可能残留的半初始化目录
		u.render(w, http.StatusInternalServerError, "repo_new", repoNewData{
			Base: u.base(r, "新建仓库"), Owners: u.ownerOptions(usr),
			Error: "git init 失败: " + err.Error(),
		})
		return
	}
	// -b needs git >= 2.28; set HEAD explicitly for wide compatibility
	_ = exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run()
	if err := u.Git.EnsureHook(repoPath); err != nil { // 运行期建仓也要装 pre-receive 钩子
		u.Log.Warn("install hook", "repo", repoPath, "err", err)
	}
	u.Log.Info("repo created", "repo", ownerName+"/"+name, "owner", owner, "public", public, "by", usr.Name)
	u.flashRedirect(w, r, "/repos/"+ownerName+"/"+name, "仓库已创建，clone 地址见仓库页")
}

// ---- git plumbing ----

func (u *UI) git(repo string, args ...string) (string, error) {
	// repo 是全名 owner/name；磁盘布局为 repos/<owner>/<name>.git
	owner, name, ok := store.SplitFullName(repo)
	if !ok {
		owner, name = "legacy", repo
	}
	args = append([]string{"--git-dir", filepath.Join(u.Home.ReposDir(), owner, name+".git")}, args...)
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	return string(out), err
}

type branchInfo struct {
	Name string
	SHA  string
}

func (u *UI) branches(repo string) ([]branchInfo, string, error) {
	out, err := u.git(repo, "for-each-ref", "--format=%(refname:short)%09%(objectname)", "refs/heads")
	if err != nil {
		return nil, "", err
	}
	var list []branchInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			list = append(list, branchInfo{Name: parts[0], SHA: parts[1]})
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	head, _ := u.git(repo, "symbolic-ref", "--short", "HEAD")
	return list, strings.TrimSpace(head), nil
}

// ---- repo pages ----

// repoHeader builds the shared repo-page header data (repohead template).
// head is the ref the tabs should point at: the repo's default branch on the
// overview page, the currently browsed branch on sub-pages.
func (u *UI) repoHeader(rp store.Repo, user, head string) RepoPage {
	_, hasSite := pipeline.SiteMeta(u.Pipe.Params.AppDirFor(rp.FullName()))
	tester := false
	if usr, ok := u.Store.GetUser(user); ok {
		tester = usr.Tester()
	}
	return RepoPage{
		Repo:      rp,
		CanManage: u.Store.CanManage(user, rp),
		CanWrite:  u.Store.CanWrite(user, rp),
		HasSite:   hasSite,
		Head:      head,
		QA:        tester,
	}
}

// blockTesterSource 对已登录的测试角色拒绝源码类页面；返回 true 表示已写出响应。
// 公共仓库的匿名浏览不受影响（匿名身份没有角色可言）。
func (u *UI) blockTesterSource(w http.ResponseWriter, r *http.Request) bool {
	if usr, _ := u.sessionUser(r); usr.Tester() {
		http.Error(w, "测试角色无权访问源码", http.StatusForbidden)
		return true
	}
	return false
}

func (u *UI) repoOverview(w http.ResponseWriter, r *http.Request) {
	rp, user, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	if usr, _ := u.sessionUser(r); usr.Tester() {
		// 测试角色的仓库页：只有部署状态与测试入口，不暴露源码相关信息
		u.repoTesterView(w, r, rp, user)
		return
	}
	brs, head, err := u.branches(rp.FullName())
	if err != nil || len(brs) == 0 {
		u.render(w, http.StatusOK, "message", messageData{Base: u.base(r, rp.Name),
			Title: "空仓库",
			Body: "仓库还没有任何提交。git clone 后推送第一个提交即可（默认分支 main）：" +
				"git clone http://" + u.base(r, "").Host + "/git/" + rp.FullName() + ".git"})
		return
	}
	readme := ""
	if data, berr := u.git(rp.FullName(), "cat-file", "blob", head+":README.md"); berr == nil && len(data) < 128*1024 {
		readme = data
	}
	bugs := u.Store.ListBugs(rp.FullName())
	if len(bugs) > 5 {
		bugs = bugs[:5]
	}
	reqs := u.Store.ListRequirements(rp.FullName())
	if len(reqs) > 5 {
		reqs = reqs[:5]
	}
	protectedNote := ""
	if strings.HasPrefix(rp.Owner, "org:") {
		protected := rp.DeployBranch
		if protected == "" {
			protected = "main"
		}
		creator := u.Store.OrgCreator(strings.TrimPrefix(rp.Owner, "org:"))
		who := creator
		if who == "" {
			who = "管理员"
		}
		protectedNote = "主分支 " + protected + " 受保护：仅组织创建者(" + who + ")与管理员可推送，其他成员请推送到自己的分支。"
	}

	var tasks []model.Task
	for _, t := range u.Hist.Recent(100) {
		if t.Repo == rp.FullName() { // Task.Repo 统一是全名 owner/name
			tasks = append(tasks, t)
			if len(tasks) >= 5 {
				break
			}
		}
	}
	meta, _ := pipeline.SiteMeta(u.Pipe.Params.AppDirFor(rp.FullName()))

	u.render(w, http.StatusOK, "repo_overview", repoOverviewData{
		Base:           u.base(r, rp.Name),
		RepoPage:       u.repoHeader(rp, user, head).WithTab("code"),
		SSHURL:         u.sshCloneURL(u.Cfg.SSHSuffix(), r.Host, rp.FullName()),
		Branches:       brs,
		README:         readme,
		RenderedReadme: renderMarkdown(readme),
		ProtectedNote:  protectedNote,
		Bugs:           bugs,
		Reqs:           reqs,
		Tasks:          tasks,
		StaticDir:      meta.StaticDir,
		Port:           meta.Port,
		APIURL:         apiURLFor(rp.Slug(), meta),
	})
}

// repoTesterView 是测试角色看到的仓库页：部署状态（版本/耗时）与测试入口
// （站点/服务端口），没有 README、分支、文件浏览、Git 地址与流水线链接。
func (u *UI) repoTesterView(w http.ResponseWriter, r *http.Request, rp store.Repo, user string) {
	var tasks []model.Task
	for _, t := range u.Hist.Recent(100) {
		if t.Repo == rp.FullName() {
			tasks = append(tasks, t)
			if len(tasks) >= 5 {
				break
			}
		}
	}
	meta, _ := pipeline.SiteMeta(u.Pipe.Params.AppDirFor(rp.FullName()))
	_, head, _ := u.branches(rp.FullName()) // 仅用于 repohead 的默认值，测试视图不显示分支
	u.render(w, http.StatusOK, "repo_tester", repoTesterData{
		Base:      u.base(r, rp.Name+" 测试"),
		RepoPage:  u.repoHeader(rp, user, head).WithTab("qa"),
		Reqs:      u.Store.ListRequirements(rp.FullName()),
		Bugs:      u.Store.ListBugs(rp.FullName()),
		APIURL:    apiURLFor(rp.Slug(), meta),
		Tasks:     tasks,
		StaticDir: meta.StaticDir,
		Port:      meta.Port,
	})
}

func (u *UI) repoCommits(w http.ResponseWriter, r *http.Request) {
	rp, user, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	if u.blockTesterSource(w, r) {
		return
	}
	// 分支名可含斜杠（feature/pdf）：{branch...} 整段作为 ref
	branch := strings.Trim(r.PathValue("branch"), "/")
	if !safeRef(branch) {
		http.Error(w, "非法的分支名", http.StatusBadRequest)
		return
	}
	out, err := u.git(rp.FullName(), "log", "--max-count=50", "--format=%H%x09%an%x09%aI%x09%s", branch)
	if err != nil {
		http.Error(w, "分支不存在: "+branch, http.StatusNotFound)
		return
	}
	var commits []commitRow
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		p := strings.SplitN(line, "\t", 4)
		if len(p) == 4 {
			when, _ := time.Parse(time.RFC3339, p[2]) // %aI 是 ISO 时间；解析失败显示 "-"
			commits = append(commits, commitRow{SHA: p[0], Author: p[1], When: when, Msg: p[3]})
		}
	}
	u.render(w, http.StatusOK, "repo_commits", repoCommitsData{
		Base:     u.base(r, rp.Name+" 提交历史"),
		RepoPage: u.repoHeader(rp, user, branch).WithTab("commits"),
		Branch:   branch,
		Commits:  commits,
	})
}

// ---- file browser (tree / blob / raw) ----

const (
	blobMaxView = 512 << 10 // 超过此大小的文件在 blob 页只提示，走 raw 下载
	rawMaxSize  = 64 << 20  // raw 端点的内存上限（cat-file 一次性读出）
)

// browserRef 校验并取出文件浏览 URL 的原始段序列（branch 与 path 通配符
// 合并）。ok=false 表示响应已写出。
func (u *UI) browserRef(w http.ResponseWriter, r *http.Request) (rp store.Repo, user string, segs []string, ok bool) {
	rp, user, ok = u.repoForRead(w, r)
	if !ok {
		return
	}
	for _, src := range []string{r.PathValue("branch"), r.PathValue("path")} {
		for _, seg := range strings.Split(strings.Trim(src, "/"), "/") {
			if seg == "" {
				continue
			}
			if !safePath(seg) {
				http.Error(w, "非法的分支名或文件路径", http.StatusBadRequest)
				ok = false
				return
			}
			segs = append(segs, seg)
		}
	}
	if len(segs) == 0 {
		// 空仓库（无任何分支）进入文件浏览：给出引导而不是 400
		u.render(w, http.StatusOK, "message", messageData{Base: u.base(r, rp.Name),
			Title: "空仓库",
			Body:  "仓库还没有任何提交。git clone 后推送第一个提交即可开始浏览代码。"})
		ok = false
	}
	return
}

// resolveRefPath 贪心地把段序列解析为 ref + 剩余路径：优先把更多段当作
// ref（与 GitHub 一致），支持 feature/pdf 这类带斜杠的分支名。
// 全部段都无法解析为 ref 时 ok=false。
func (u *UI) resolveRefPath(full string, segs []string) (ref, p string, ok bool) {
	for k := len(segs); k >= 1; k-- {
		cand := strings.Join(segs[:k], "/")
		if !safeRef(cand) {
			continue
		}
		if _, err := u.git(full, "rev-parse", "--verify", cand+"^{commit}"); err == nil {
			return cand, strings.Join(segs[k:], "/"), true
		}
	}
	return "", "", false
}

// apiURLFor 有后端服务时返回 API 代理路径（/api/<slug>/），否则空。
func apiURLFor(slug string, meta pipeline.SiteInfo) string {
	if meta.Port <= 0 {
		return ""
	}
	return "/api/" + slug + "/"
}

// crumbTrail 生成某分支下路径的面包屑（不含分支本身，分支由模板单独渲染）。
func crumbTrail(repoFull, branch, p string) []crumb {
	var cs []crumb
	if p == "" {
		return cs
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		c := crumb{Name: s, Final: i == len(segs)-1}
		if !c.Final {
			c.URL = "/repos/" + repoFull + "/tree/" + branch + "/" + strings.Join(segs[:i+1], "/")
		}
		cs = append(cs, c)
	}
	return cs
}

// parseLsTree 解析 `git ls-tree -l -z` 输出，记录格式为
// "<mode> <type> <sha> <size>\t<name>\0"（size 对 tree 为 "-"）。
// 返回按目录优先、名称排序的条目。
func parseLsTree(out, full, branch, dir string) []treeEntry {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	var entries []treeEntry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, name, _ := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if len(f) < 3 {
			continue
		}
		e := treeEntry{Name: name, Size: "-"}
		switch f[1] {
		case "tree", "commit": // commit = 子模块，按目录占位展示
			e.IsDir = true
			e.URL = "/repos/" + full + "/tree/" + branch + "/" + prefix + name
		case "blob":
			if len(f) >= 4 && f[3] != "-" {
				if n, err := strconv.ParseInt(f[3], 10, 64); err == nil {
					e.Size = humanSize(n)
				}
			}
			e.URL = "/repos/" + full + "/blob/" + branch + "/" + prefix + name
		default:
			continue
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func (u *UI) repoTree(w http.ResponseWriter, r *http.Request) {
	rp, user, segs, ok := u.browserRef(w, r)
	if !ok {
		return
	}
	if u.blockTesterSource(w, r) {
		return
	}
	full := rp.FullName()
	// ref 可以是分支名（含斜杠）、tag 或提交 sha；贪心解析（GitHub 语义）
	ref, p, resolved := u.resolveRefPath(full, segs)
	if !resolved {
		u.render(w, http.StatusNotFound, "message", messageData{Base: u.base(r, rp.Name),
			Title: "分支不存在",
			Body:  "分支或提交 " + strings.Join(segs, "/") + " 不存在（或仓库还没有提交）。"})
		return
	}
	treeish := ref
	if p != "" {
		typ, err := u.git(full, "cat-file", "-t", ref+":"+p)
		switch {
		case err == nil && strings.TrimSpace(typ) == "blob":
			http.Redirect(w, r, "/repos/"+full+"/blob/"+ref+"/"+p, http.StatusFound)
			return
		case err == nil && strings.TrimSpace(typ) == "tree":
			treeish = ref + ":" + p
		default:
			// 路径在该 ref 下不存在（常见于切换分支后原路径消失）：回到该分支根目录
			u.flashRedirect(w, r, "/repos/"+full+"/tree/"+ref,
				"路径 "+p+" 在 "+ref+" 中不存在，已回到根目录")
			return
		}
	}
	out, err := u.git(full, "ls-tree", "-l", "-z", treeish)
	if err != nil {
		http.Error(w, "读取目录失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	brs, _, _ := u.branches(full)
	u.render(w, http.StatusOK, "repo_tree", repoTreeData{
		Base:     u.base(r, rp.Name+" 代码"),
		RepoPage: u.repoHeader(rp, user, ref).WithTab("files"),
		Branch:   ref,
		Path:     p,
		Crumbs:   crumbTrail(full, ref, p),
		Entries:  parseLsTree(out, full, ref, p),
		Branches: brs,
	})
}

func (u *UI) repoBlob(w http.ResponseWriter, r *http.Request) {
	rp, user, segs, ok := u.browserRef(w, r)
	if !ok {
		return
	}
	if u.blockTesterSource(w, r) {
		return
	}
	full := rp.FullName()
	ref, p, resolved := u.resolveRefPath(full, segs)
	if !resolved {
		u.render(w, http.StatusNotFound, "message", messageData{Base: u.base(r, rp.Name),
			Title: "分支不存在",
			Body:  "分支或提交 " + strings.Join(segs, "/") + " 不存在。"})
		return
	}
	if p == "" { // blob 必须有具体路径：回到目录
		http.Redirect(w, r, "/repos/"+full+"/tree/"+ref, http.StatusFound)
		return
	}
	typ, err := u.git(full, "cat-file", "-t", ref+":"+p)
	switch {
	case err == nil && strings.TrimSpace(typ) == "tree": // 目录 URL 误入 blob：跳到目录页
		http.Redirect(w, r, "/repos/"+full+"/tree/"+ref+"/"+p, http.StatusFound)
		return
	case err != nil || strings.TrimSpace(typ) != "blob":
		u.render(w, http.StatusNotFound, "message", messageData{Base: u.base(r, rp.Name),
			Title: "文件不存在",
			Body:  "分支 " + ref + " 下没有文件 " + p + "。"})
		return
	}
	sizeOut, _ := u.git(full, "cat-file", "-s", ref+":"+p)
	size, _ := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64)

	dir := path.Dir(p)
	if dir == "." {
		dir = ""
	}
	brs, _, _ := u.branches(full)
	d := repoBlobData{
		Base:     u.base(r, rp.Name+" · "+path.Base(p)),
		RepoPage: u.repoHeader(rp, user, ref).WithTab("files"),
		Branch:   ref,
		Path:     p,
		Dir:      dir,
		Language: codeLang(p),
		Crumbs:   crumbTrail(full, ref, p),
		Size:     size,
		Branches: brs,
	}
	switch {
	case size > blobMaxView:
		d.TooBig = true
	default:
		data, err := u.git(full, "cat-file", "blob", ref+":"+p)
		if err != nil {
			http.Error(w, "读取文件失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if isBinary([]byte(data)) {
			d.Binary = true
		} else {
			d.Content = data
			d.Lines = strings.Count(data, "\n")
			if len(data) > 0 && !strings.HasSuffix(data, "\n") {
				d.Lines++
			}
		}
	}
	u.render(w, http.StatusOK, "repo_blob", d)
}

// isBinary 粗判二进制：前 8 KiB 含 NUL 即视为二进制。
func isBinary(data []byte) bool {
	if len(data) > 8000 {
		data = data[:8000]
	}
	return bytes.IndexByte(data, 0) >= 0
}

// codeLang 按扩展名返回浏览器端语法高亮的语言标识（blobJS 支持的 key），
// 空串表示使用通用规则（字符串/数字/注释）。
func codeLang(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".js", ".mjs", ".cjs":
		return "js"
	case ".ts", ".tsx", ".mts", ".cts":
		return "ts"
	case ".py", ".pyw":
		return "py"
	case ".java":
		return "java"
	case ".c", ".h":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hh":
		return "cpp"
	case ".rs":
		return "rust"
	case ".sh", ".bash", ".zsh":
		return "sh"
	case ".sql":
		return "sql"
	case ".yaml", ".yml":
		return "yaml"
	case ".json":
		return "json"
	case ".toml":
		return "toml"
	case ".ini", ".cfg", ".conf", ".properties":
		return "ini"
	case ".css", ".scss", ".less":
		return "css"
	case ".html", ".htm", ".xml", ".svg", ".vue":
		return "html"
	case ".rb":
		return "rb"
	case ".php":
		return "php"
	}
	return ""
}

func (u *UI) repoRaw(w http.ResponseWriter, r *http.Request) {
	rp, _, segs, ok := u.browserRef(w, r)
	if !ok {
		return
	}
	if u.blockTesterSource(w, r) {
		return
	}
	full := rp.FullName()
	ref, p, resolved := u.resolveRefPath(full, segs)
	if !resolved {
		http.NotFound(w, r)
		return
	}
	if p == "" {
		http.Redirect(w, r, "/repos/"+full+"/tree/"+ref, http.StatusFound)
		return
	}
	typ, err := u.git(full, "cat-file", "-t", ref+":"+p)
	if err != nil || strings.TrimSpace(typ) != "blob" {
		http.NotFound(w, r)
		return
	}
	sizeOut, _ := u.git(full, "cat-file", "-s", ref+":"+p)
	if size, serr := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64); serr == nil && size > rawMaxSize {
		http.Error(w, "文件过大（>64 MiB），拒绝通过 raw 提供", http.StatusRequestEntityTooLarge)
		return
	}
	data, err := u.git(full, "cat-file", "blob", ref+":"+p)
	if err != nil {
		http.Error(w, "读取文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	b := []byte(data)
	head := b
	if len(head) > 512 {
		head = head[:512]
	}
	w.Header().Set("Content-Type", http.DetectContentType(head))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": path.Base(p)}))
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

// ---- repo settings ----

func (u *UI) repoSettingsPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireDeveloper(w, r); !ok {
		return
	}
	rp, user, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	if !u.Store.CanManage(user, rp) {
		http.Error(w, "无权管理该仓库", http.StatusForbidden)
		return
	}
	_, head, _ := u.branches(rp.FullName())
	u.render(w, http.StatusOK, "repo_settings", repoSettingsData{
		Base:     u.base(r, rp.Name+" 设置"),
		RepoPage: u.repoHeader(rp, user, head).WithTab("settings"),
	})
}

func (u *UI) repoSettingsPost(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	full := owner + "/" + name
	rp, ok := u.Store.GetRepo(full)
	if !ok {
		http.NotFound(w, r)
		return
	}
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return
	}
	if !u.Store.CanManage(usr.Name, rp) {
		http.Error(w, "无权管理该仓库", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	switch r.PostFormValue("action") {
	case "update":
		visibility := r.PostFormValue("visibility") == "public"
		branch := strings.TrimSpace(r.PostFormValue("deploy_branch"))
		if branch != "" && !repoNameRE.MatchString(branch) {
			http.Error(w, "分支名不合法", http.StatusBadRequest)
			return
		}
		err := u.Store.UpdateRepo(full, func(m *store.Repo) error {
			m.Public = visibility
			m.DeployBranch = branch
			return nil
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		u.flashRedirect(w, r, "/repos/"+full+"/settings", "设置已保存")
	case "delete":
		if err := u.Store.DeleteRepo(full); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// 级联清理：缺陷/需求记录与附件、git 数据、工作区、部署产物
		if err := u.Store.PurgeRepoData(full); err != nil {
			u.Log.Warn("purge repo data", "repo", full, "err", err)
		}
		_ = os.RemoveAll(filepath.Join(u.Home.DataDir(), "bugfiles", pipeline.RepoSlug(full)))
		_ = os.RemoveAll(u.Pipe.Params.RepoPath(full))
		_ = os.RemoveAll(u.Pipe.Params.WorkdirFor(full))
		_ = os.RemoveAll(u.Pipe.Params.AppDirFor(full))
		u.Log.Info("repo deleted", "repo", name, "by", usr.Name)
		u.flashRedirect(w, r, "/", "仓库已删除")
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// repoDeployPost triggers a manual deployment (latest commit of the deploy
// branch, or the requested branch).
func (u *UI) repoDeployPost(w http.ResponseWriter, r *http.Request) {
	rp, user, ok := u.repoForRead(w, r)
	full := rp.FullName()
	if !ok {
		return
	}
	if !u.Store.CanWrite(user, rp) {
		http.Error(w, "无权部署该仓库", http.StatusForbidden)
		return
	}
	branch := rp.DeployBranch
	if branch == "" {
		if _, head, err := u.branches(rp.FullName()); err == nil && head != "" {
			branch = head
		}
	}
	if branch == "" {
		u.flashRedirect(w, r, "/repos/"+full, "仓库还没有任何分支")
		return
	}
	// sha 可选：指定时部署该历史提交（回滚）；缺省部署分支最新
	sha := strings.TrimSpace(r.PostFormValue("sha"))
	if sha != "" {
		if !safeRef(sha) {
			http.Error(w, "非法的提交标识", http.StatusBadRequest)
			return
		}
		if _, err := u.git(rp.FullName(), "cat-file", "-e", sha+"^{commit}"); err != nil {
			u.flashRedirect(w, r, "/repos/"+full, "提交 "+sha+" 在仓库中不存在")
			return
		}
	} else {
		out, err := u.git(rp.FullName(), "rev-parse", "refs/heads/"+branch)
		if err != nil {
			u.flashRedirect(w, r, "/repos/"+full, "分支 "+branch+" 不存在")
			return
		}
		sha = strings.TrimSpace(out)
	}
	// Task.Repo 统一用全名 owner/name：与 push 触发的任务一致，
	// 仓库作用域的流水线过滤和 /repos/<Task.Repo> 反链都依赖这一点。
	task := u.Pipe.NewTask(rp.FullName(), branch, sha)
	u.Hist.Add(task)
	if _, err := u.Queue.Enqueue(task); err != nil {
		task.Status = model.StatusSkipped
		task.Error = err.Error()
		task.FinishedAt = time.Now()
		u.Hist.Persist(task)
		u.flashRedirect(w, r, "/pipelines/"+task.ID, err.Error())
		return
	}
	u.Log.Info("manual deploy queued", "task", task.ID, "repo", rp.FullName(), "branch", branch, "by", user)
	u.flashRedirect(w, r, "/pipelines/"+task.ID, "部署任务已创建")
}
