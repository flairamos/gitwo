package webui

import (
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitwo/internal/model"
	"gitwo/internal/store"
)

// 缺陷上传限制。
const (
	bugMaxFiles     = 9
	bugMaxFileBytes = 8 << 20  // 单张截图 8 MiB
	bugMaxFormBytes = 24 << 20 // 整个表单 24 MiB
)

// bugFilesDir 是某仓库某缺陷的截图目录：data/bugfiles/<repo-slug>/<bug-id>/。
// 放在 data/ 下使"备份 = 拷目录"继续成立。
func (u *UI) bugFilesDir(repoSlug, bugID string) string {
	return filepath.Join(u.Home.DataDir(), "bugfiles", repoSlug, bugID)
}

// bugCandidates 列出可被指派修复缺陷的用户：对该仓库有写权限的用户
// （归属者/组织成员）与管理员；测试角色没有写权限，天然不在列表里。
func (u *UI) bugCandidates(rp store.Repo) []store.User {
	var out []store.User
	for _, usr := range u.Store.ListUsers() {
		if usr.Tester() {
			continue
		}
		if u.Store.CanWrite(usr.Name, rp) {
			out = append(out, usr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// composeBugBody 把表单字段组装成统一的 markdown 缺陷文档。
func composeBugBody(steps, expected, actual, note string) string {
	trim := func(s string) string { return strings.TrimSpace(s) }
	var b strings.Builder
	b.WriteString("## 复现步骤\n\n")
	b.WriteString(trim(steps) + "\n\n")
	b.WriteString("## 预期结果\n\n")
	if e := trim(expected); e != "" {
		b.WriteString(e + "\n\n")
	} else {
		b.WriteString("—\n\n")
	}
	b.WriteString("## 实际结果\n\n")
	if a := trim(actual); a != "" {
		b.WriteString(a + "\n\n")
	} else {
		b.WriteString("—\n\n")
	}
	if n := trim(note); n != "" {
		b.WriteString("## 备注\n\n")
		b.WriteString(n + "\n\n")
	}
	return b.String()
}

// ---- 缺陷列表 ----

func (u *UI) repoBugsPage(w http.ResponseWriter, r *http.Request) {
	rp, user, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	full := rp.FullName()
	bugs := u.Store.ListBugs(full)
	filter := r.URL.Query().Get("status")
	if filter != "" && store.ValidBugStatus(filter) {
		var kept []store.Bug
		for _, b := range bugs {
			if b.Status == filter {
				kept = append(kept, b)
			}
		}
		bugs = kept
	}
	openN, fixedN, closedN := 0, 0, 0
	for _, b := range u.Store.ListBugs(full) {
		switch b.Status {
		case store.BugOpen:
			openN++
		case store.BugFixed:
			fixedN++
		case store.BugClosed:
			closedN++
		}
	}
	usr, _ := u.sessionUser(r)
	u.render(w, http.StatusOK, "repo_bugs", repoBugsData{
		Base:     u.base(r, rp.Name+" 缺陷"),
		RepoPage: u.repoHeader(rp, user, "").WithTab("bugs"),
		Bugs:     bugs,
		Filter:   filter,
		Open:     openN, Fixed: fixedN, Closed: closedN,
		Tester: usr.Tester(),
	})
}

// ---- 提交缺陷 ----

func (u *UI) bugNewPage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	rp, _, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	u.render(w, http.StatusOK, "bug_new", bugNewData{
		Base:       u.base(r, "提交缺陷 · "+rp.Name),
		RepoPage:   u.repoHeader(rp, usr.Name, "").WithTab("bugs"),
		Candidates: u.bugCandidates(rp),
		Error:      r.URL.Query().Get("err"),
	})
}

func (u *UI) bugNewPost(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	rp, _, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	full := rp.FullName()
	fail := func(msg string) {
		http.Redirect(w, r, "/repos/"+full+"/bugs/new?err="+url.QueryEscape(msg), http.StatusFound)
	}
	if !parseAnyFormSilent(r, bugMaxFormBytes) {
		fail("上传内容过大或表单格式错误（表单上限 24 MiB）")
		return
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	severity := r.PostFormValue("severity")
	assignee := strings.TrimSpace(r.PostFormValue("assignee"))
	steps := strings.TrimSpace(r.PostFormValue("steps"))
	if title == "" || steps == "" {
		fail("标题与复现步骤不能为空")
		return
	}
	if !store.ValidSeverity(severity) {
		severity = store.SeverityMajor
	}
	// 指派对象必须是能修这个仓库的人（有写权限且不是测试角色）
	validAssignee := false
	for _, c := range u.bugCandidates(rp) {
		if c.Name == assignee {
			validAssignee = true
			break
		}
	}
	if !validAssignee {
		fail("指派对象无效：请选择对该仓库有开发权限的人员")
		return
	}

	// 截图：先存盘（bug ID 由 handler 生成，附件目录随之确定），再建单
	bug := store.Bug{
		Repo:     full,
		Title:    title,
		Body:     composeBugBody(steps, r.PostFormValue("expected"), r.PostFormValue("actual"), r.PostFormValue("note")),
		Severity: severity,
		Status:   store.BugOpen,
		Reporter: usr.Name,
		Assignee: assignee,
		ID:       model.NewID(),
	}
	dir := u.bugFilesDir(rp.Slug(), bug.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail("创建附件目录失败: " + err.Error())
		return
	}
	created := false
	defer func() {
		if !created {
			os.RemoveAll(dir) // 提交失败不留孤儿附件
		}
	}()

	var saved []string
	for i := 0; i < bugMaxFiles; i++ {
		f, hdr, err := r.FormFile(fmt.Sprintf("shot%d", i))
		if err != nil {
			break // 没有更多文件
		}
		data, rerr := io.ReadAll(f)
		f.Close()
		if rerr != nil || len(data) > bugMaxFileBytes {
			fail("截图 " + hdr.Filename + " 读取失败或超过 8 MiB")
			return
		}
		ct := http.DetectContentType(data)
		ext := ""
		switch {
		case strings.HasPrefix(ct, "image/png"):
			ext = ".png"
		case strings.HasPrefix(ct, "image/jpeg"):
			ext = ".jpg"
		case strings.HasPrefix(ct, "image/gif"):
			ext = ".gif"
		case strings.HasPrefix(ct, "image/webp"):
			ext = ".webp"
		}
		if ext == "" {
			fail("截图 " + hdr.Filename + " 不是支持的图片格式（png/jpeg/gif/webp）")
			return
		}
		name := fmt.Sprintf("%d%s", i, ext)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			fail("保存截图失败: " + err.Error())
			return
		}
		saved = append(saved, name)
	}
	bug.Files = saved

	if err := u.Store.CreateBug(&bug); err != nil {
		fail("提交失败: " + err.Error())
		return
	}
	created = true
	u.Log.Info("bug filed", "bug", bug.ID, "repo", full, "by", usr.Name, "assignee", bug.Assignee)
	u.flashRedirect(w, r, "/repos/"+full+"/bugs/"+bug.ID, "缺陷已提交给 "+bug.Assignee)
}

// ---- 缺陷详情 ----

func (u *UI) bugDetail(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	bug, user, ok := u.bugForRead(w, r, owner+"/"+name)
	if !ok {
		return
	}
	usr, _ := u.sessionUser(r)
	isAdmin := usr.Admin
	// 状态流转权限：开发修复 → 待复测；报告人复测 → 通过关闭 / 不通过重开
	canFix := bug.Status == store.BugOpen && (isAdmin || user == bug.Assignee)
	canVerify := bug.Status == store.BugFixed && (isAdmin || user == bug.Reporter)
	canCloseInvalid := bug.Status == store.BugOpen && (isAdmin || user == bug.Reporter)

	rp, _ := u.Store.GetRepo(bug.Repo)
	u.render(w, http.StatusOK, "bug_detail", bugDetailData{
		Base:            u.base(r, bug.Title),
		RepoPage:        u.repoHeader(rp, user, "").WithTab("bugs"),
		Bug:             bug,
		RenderedBody:    renderMarkdown(bug.Body),
		Comments:        renderBugComments(bug.Comments),
		CanFix:          canFix,
		CanVerify:       canVerify,
		CanCloseInvalid: canCloseInvalid,
		FilesURLPrefix:  "/repos/" + bug.Repo + "/bugs/" + bug.ID + "/files/",
	})
}

type renderedComment struct {
	store.BugComment
	HTML template.HTML
}

func renderBugComments(cs []store.BugComment) []renderedComment {
	out := make([]renderedComment, 0, len(cs))
	for _, c := range cs {
		out = append(out, renderedComment{BugComment: c, HTML: renderMarkdown(c.Body)})
	}
	return out
}

// bugForRead 取出属于当前仓库的缺陷并校验仓库读权限；仓库或归属不匹配一律
// 404 —— 缺陷严格隔离在所属仓库内。
func (u *UI) bugForRead(w http.ResponseWriter, r *http.Request, full string) (store.Bug, string, bool) {
	rp, ok := u.Store.GetRepo(full)
	if !ok {
		http.NotFound(w, r)
		return store.Bug{}, "", false
	}
	bug, ok := u.Store.GetBug(r.PathValue("id"))
	if !ok || bug.Repo != full {
		http.NotFound(w, r)
		return store.Bug{}, "", false
	}
	usr, _ := u.sessionUser(r)
	if !u.Store.CanRead(usr.Name, rp) {
		if usr.Name == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
		} else {
			http.Error(w, "无权查看该仓库的缺陷", http.StatusForbidden)
		}
		return store.Bug{}, "", false
	}
	return bug, usr.Name, true
}

// ---- 状态流转与评论 ----

func (u *UI) bugStatusPost(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	full := owner + "/" + name
	bug, user, ok := u.bugForRead(w, r, full)
	if !ok {
		return
	}
	usr, _ := u.sessionUser(r)
	to := r.PostFormValue("to")
	if !store.ValidBugStatus(to) || to == bug.Status {
		http.Error(w, "invalid status transition", http.StatusBadRequest)
		return
	}
	isAdmin := usr.Admin
	switch to {
	case store.BugFixed:
		if bug.Status != store.BugOpen || (!isAdmin && user != bug.Assignee) {
			http.Error(w, "只有被指派的开发人员可将缺陷标记为待复测", http.StatusForbidden)
			return
		}
	case store.BugClosed:
		if !isAdmin && user != bug.Reporter {
			http.Error(w, "只有提交人可关闭缺陷", http.StatusForbidden)
			return
		}
	case store.BugOpen:
		if bug.Status != store.BugFixed || (!isAdmin && user != bug.Reporter) {
			http.Error(w, "只有提交人可复测不通过并重新打开", http.StatusForbidden)
			return
		}
	}
	from := bug.Status
	err := u.Store.UpdateBug(bug.ID, func(b *store.Bug) error {
		b.Status = to
		if to == store.BugClosed {
			b.ClosedAt = time.Now()
		}
		b.Comments = append(b.Comments, store.BugComment{
			Author: user,
			Body:   statusChangeNote(from, to, r.PostFormValue("note")),
			Time:   time.Now(),
			Status: from + ">" + to,
		})
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.Log.Info("bug status", "bug", bug.ID, "from", from, "to", to, "by", user)
	u.flashRedirect(w, r, "/repos/"+full+"/bugs/"+bug.ID, store.BugStatusText(from)+" → "+store.BugStatusText(to))
}

// statusChangeNote 组装状态流转的系统评论（markdown）。
func statusChangeNote(from, to, note string) string {
	var b strings.Builder
	b.WriteString("> 状态流转：**" + store.BugStatusText(from) + "** → **" + store.BugStatusText(to) + "**\n")
	if n := strings.TrimSpace(note); n != "" {
		b.WriteString("\n" + n + "\n")
	}
	if to == store.BugFixed {
		b.WriteString("\n开发已完成修复并重新部署，等待测试复测。\n")
	}
	if to == store.BugClosed {
		b.WriteString("\n复测通过，缺陷永久关闭。\n")
	}
	return b.String()
}

func (u *UI) bugCommentPost(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	full := owner + "/" + name
	bug, user, ok := u.bugForRead(w, r, full)
	if !ok {
		return
	}
	body := strings.TrimSpace(r.PostFormValue("body"))
	if body == "" {
		u.flashRedirect(w, r, "/repos/"+full+"/bugs/"+bug.ID, "评论内容不能为空")
		return
	}
	if len(body) > 16*1024 {
		http.Error(w, "评论过长", http.StatusBadRequest)
		return
	}
	err := u.Store.UpdateBug(bug.ID, func(b *store.Bug) error {
		b.Comments = append(b.Comments, store.BugComment{Author: user, Body: body, Time: time.Now()})
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.flashRedirect(w, r, "/repos/"+full+"/bugs/"+bug.ID, "评论已添加")
}

// ---- 截图 ----

// bugFileServe 输出缺陷截图；文件名必须严格出现在 bug.Files 里（防穿越）。
func (u *UI) bugFileServe(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	full := owner + "/" + name
	bug, _, ok := u.bugForRead(w, r, full)
	if !ok {
		return
	}
	file := r.PathValue("file")
	known := false
	for _, f := range bug.Files {
		if f == file {
			known = true
			break
		}
	}
	if !known {
		http.NotFound(w, r)
		return
	}
	rp, _ := u.Store.GetRepo(bug.Repo)
	data, err := os.ReadFile(filepath.Join(u.bugFilesDir(rp.Slug(), bug.ID), file))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ct := "application/octet-stream"
	switch path.Ext(file) {
	case ".png":
		ct = "image/png"
	case ".jpg":
		ct = "image/jpeg"
	case ".gif":
		ct = "image/gif"
	case ".webp":
		ct = "image/webp"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(data)
}
