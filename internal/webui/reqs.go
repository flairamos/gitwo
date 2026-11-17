package webui

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitwo/internal/store"
)

// 需求模块：仓库级迭代需求文档。任何可读者可见（同组织任何角色），
// 内容只有创建者可编辑；状态流转见 store/requirement.go 的状态机注释。

// reqForRead 取出属于当前仓库的需求并校验仓库读权限；仓库或归属不匹配
// 一律 404 —— 需求严格隔离在所属仓库内。
func (u *UI) reqForRead(w http.ResponseWriter, r *http.Request, full string) (store.Requirement, string, bool) {
	rp, ok := u.Store.GetRepo(full)
	if !ok {
		http.NotFound(w, r)
		return store.Requirement{}, "", false
	}
	req, ok := u.Store.GetRequirement(r.PathValue("id"))
	if !ok || req.Repo != full {
		http.NotFound(w, r)
		return store.Requirement{}, "", false
	}
	usr, _ := u.sessionUser(r)
	if !u.Store.CanRead(usr.Name, rp) {
		if usr.Name == "" {
			http.Redirect(w, r, "/login", http.StatusFound)
		} else {
			http.Error(w, "无权查看该仓库的需求", http.StatusForbidden)
		}
		return store.Requirement{}, "", false
	}
	return req, usr.Name, true
}

// reqCanMarkReview 报告谁可以把需求标记为待核查：对该仓库有写权限的人
// （即允许推送代码的"代码提交者"）与管理员。
func (u *UI) reqCanMarkReview(user string, rp store.Repo) bool {
	return u.Store.CanWrite(user, rp)
}

// ---- 需求列表（标题平铺） ----

func (u *UI) repoReqsPage(w http.ResponseWriter, r *http.Request) {
	rp, user, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	full := rp.FullName()
	reqs := u.Store.ListRequirements(full)
	filter := r.URL.Query().Get("status")
	if filter != "" && store.ValidReqStatus(filter) {
		var kept []store.Requirement
		for _, q := range reqs {
			if q.Status == filter {
				kept = append(kept, q)
			}
		}
		reqs = kept
	}
	pendingN, reviewN, closedN := 0, 0, 0
	for _, q := range u.Store.ListRequirements(full) {
		switch q.Status {
		case store.ReqPending:
			pendingN++
		case store.ReqReview:
			reviewN++
		case store.ReqClosed:
			closedN++
		}
	}
	u.render(w, http.StatusOK, "repo_reqs", repoReqsData{
		Base:     u.base(r, rp.Name+" 需求"),
		RepoPage: u.repoHeader(rp, user, "").WithTab("reqs"),
		Reqs:     reqs,
		Filter:   filter,
		Pending:  pendingN, Review: reviewN, Closed: closedN,
	})
}

// ---- 新建需求 ----

func (u *UI) reqNewPage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	rp, _, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	u.render(w, http.StatusOK, "req_edit", reqEditData{
		Base:     u.base(r, "新建需求 · "+rp.Name),
		RepoPage: u.repoHeader(rp, usr.Name, "").WithTab("reqs"),
		IsNew:    true,
		Error:    r.URL.Query().Get("err"),
		Title:    r.URL.Query().Get("title"),
		Body:     r.URL.Query().Get("body"),
	})
}

func (u *UI) reqCreatePost(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	rp, _, ok := u.repoForRead(w, r)
	if !ok {
		return
	}
	full := rp.FullName()
	if !parseAnyForm(w, r, 8<<20) {
		return
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	if title == "" || body == "" {
		u.flashRedirect(w, r, "/repos/"+full+"/reqs/new?err="+url.QueryEscape("标题与内容不能为空"), "")
		return
	}
	req := store.Requirement{
		Repo:    full,
		Title:   title,
		Body:    body,
		Status:  store.ReqPending,
		Creator: usr.Name,
	}
	if err := u.Store.CreateRequirement(&req); err != nil {
		u.flashRedirect(w, r, "/repos/"+full+"/reqs/new?err="+url.QueryEscape("提交失败: "+err.Error()), "")
		return
	}
	u.Log.Info("requirement created", "req", req.ID, "repo", full, "by", usr.Name)
	u.flashRedirect(w, r, "/repos/"+full+"/reqs/"+req.ID, "需求已创建（待开发）")
}

// ---- 需求详情 ----

func (u *UI) reqDetail(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	req, user, ok := u.reqForRead(w, r, owner+"/"+name)
	if !ok {
		return
	}
	rp, _ := u.Store.GetRepo(req.Repo)
	canEdit := user == req.Creator // 只有创建者可编辑内容
	canReview := req.Status == store.ReqPending && u.reqCanMarkReview(user, rp)
	canClose := req.Creator == user // 只有创建者可关闭（验收通过/作废）
	canReject := req.Status == store.ReqReview && req.Creator == user
	u.render(w, http.StatusOK, "req_detail", reqDetailData{
		Base:         u.base(r, req.Title),
		RepoPage:     u.repoHeader(rp, user, "").WithTab("reqs"),
		Req:          req,
		RenderedBody: renderMarkdown(req.Body),
		Records:      req.Records,
		CanEdit:      canEdit,
		CanReview:    canReview,
		CanClose:     canClose,
		CanReject:    canReject,
	})
}

// ---- 编辑（仅创建者） ----

func (u *UI) reqEditPage(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	req, user, ok := u.reqForRead(w, r, owner+"/"+name)
	if !ok {
		return
	}
	if user != req.Creator {
		http.Error(w, "只有需求的创建者可以编辑内容", http.StatusForbidden)
		return
	}
	rp, _ := u.Store.GetRepo(req.Repo)
	u.render(w, http.StatusOK, "req_edit", reqEditData{
		Base:     u.base(r, "编辑需求 · "+req.Title),
		RepoPage: u.repoHeader(rp, user, "").WithTab("reqs"),
		Req:      req,
		Title:    req.Title,
		Body:     req.Body,
	})
}

func (u *UI) reqEditPost(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	full := owner + "/" + name
	req, user, ok := u.reqForRead(w, r, full)
	if !ok {
		return
	}
	if user != req.Creator {
		http.Error(w, "只有需求的创建者可以编辑内容", http.StatusForbidden)
		return
	}
	if !parseAnyForm(w, r, 8<<20) {
		return
	}
	title := strings.TrimSpace(r.PostFormValue("title"))
	body := strings.TrimSpace(r.PostFormValue("body"))
	if title == "" || body == "" {
		u.flashRedirect(w, r, "/repos/"+full+"/reqs/"+req.ID+"/edit?err="+url.QueryEscape("标题与内容不能为空"), "")
		return
	}
	err := u.Store.UpdateRequirement(req.ID, func(q *store.Requirement) error {
		q.Title = title
		q.Body = body
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.Log.Info("requirement edited", "req", req.ID, "by", user)
	u.flashRedirect(w, r, "/repos/"+full+"/reqs/"+req.ID, "需求已更新")
}

// ---- 状态流转 ----

func (u *UI) reqStatusPost(w http.ResponseWriter, r *http.Request) {
	owner, name := r.PathValue("owner"), r.PathValue("name")
	full := owner + "/" + name
	req, user, ok := u.reqForRead(w, r, full)
	if !ok {
		return
	}
	usr, _ := u.sessionUser(r)
	to := r.PostFormValue("to")
	if !store.ValidReqStatus(to) || to == req.Status {
		http.Error(w, "invalid status transition", http.StatusBadRequest)
		return
	}
	rp, _ := u.Store.GetRepo(full)
	isAdmin := usr.Admin
	switch to {
	case store.ReqReview:
		// 任何代码提交者（对该仓库有写权限的人）/管理员：功能已实现，提交核查
		if req.Status != store.ReqPending || !u.reqCanMarkReview(user, rp) {
			http.Error(w, "只有对该仓库有开发权限的人员（代码提交者）可将需求标记为待核查", http.StatusForbidden)
			return
		}
	case store.ReqClosed:
		// 只有创建者可以关闭（验收通过 或 直接作废）
		if req.Creator != user && !isAdmin {
			http.Error(w, "只有需求的创建者可以关闭需求", http.StatusForbidden)
			return
		}
	case store.ReqPending:
		// 验收不通过：创建者退回待开发
		if req.Status != store.ReqReview || (req.Creator != user && !isAdmin) {
			http.Error(w, "只有需求的创建者可以在核查不通过时退回待开发", http.StatusForbidden)
			return
		}
	}
	from := req.Status
	err := u.Store.UpdateRequirement(req.ID, func(q *store.Requirement) error {
		q.Status = to
		if to == store.ReqClosed {
			q.ClosedAt = time.Now()
		}
		q.Records = append(q.Records, store.ReqRecord{
			Author: user,
			Body:   reqStatusNote(from, to, r.PostFormValue("note")),
			Status: from + ">" + to,
		})
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	u.Log.Info("requirement status", "req", req.ID, "from", from, "to", to, "by", user)
	u.flashRedirect(w, r, "/repos/"+full+"/reqs/"+req.ID, store.ReqStatusText(from)+" → "+store.ReqStatusText(to))
}

// reqStatusNote 组装状态流转的系统记录（markdown）。
func reqStatusNote(from, to, note string) string {
	var b strings.Builder
	b.WriteString("> 状态流转：**" + store.ReqStatusText(from) + "** → **" + store.ReqStatusText(to) + "**\n")
	if n := strings.TrimSpace(note); n != "" {
		b.WriteString("\n" + n + "\n")
	}
	if to == store.ReqReview {
		b.WriteString("\n功能已开发完成并部署，等待创建者核查。\n")
	}
	if to == store.ReqClosed {
		b.WriteString("\n需求已关闭。\n")
	}
	return b.String()
}
