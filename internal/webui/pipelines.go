package webui

import (
	"net/http"

	"gitwo/internal/fsutil"
	"gitwo/internal/model"
	"gitwo/internal/store"
)

// pipelinesPage renders the pipeline list. Without ?repo= it is the global
// view, filtered to repos the current user can read (admins see everything).
// With ?repo=owner/name it is the repo-scoped view: a repo page only leads
// to its own pipelines, never the global list.
func (u *UI) pipelinesPage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return
	}
	if scope := r.URL.Query().Get("repo"); scope != "" {
		u.repoPipelines(w, r, usr, scope)
		return
	}

	visible := map[string]bool{}
	for _, rp := range u.Store.VisibleRepos(usr.Name) {
		visible[rp.FullName()] = true
	}
	var tasks []model.Task
	for _, t := range u.Hist.Recent(200) {
		if usr.Admin || visible[t.Repo] {
			tasks = append(tasks, t)
			if len(tasks) >= 50 {
				break
			}
		}
	}
	u.render(w, http.StatusOK, "pipelines", pipelinesData{
		Base:    u.base(r, "流水线"),
		Tasks:   tasks,
		Running: u.Hist.Count(model.StatusRunning),
		Pending: u.Queue.Pending(),
		Msg:     r.URL.Query().Get("msg"),
	})
}

// repoPipelines 是仓库作用域的流水线列表：仅该仓库的任务，且要求对仓库有
// 读权限；页面不提供进入全局流水线的入口。
func (u *UI) repoPipelines(w http.ResponseWriter, r *http.Request, usr store.User, full string) {
	rp, ok := u.Store.GetRepo(full)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !u.Store.CanRead(usr.Name, rp) {
		http.Error(w, "无权查看该仓库的流水线", http.StatusForbidden)
		return
	}
	var tasks []model.Task
	for _, t := range u.Hist.Recent(200) {
		if t.Repo == rp.FullName() {
			tasks = append(tasks, t)
			if len(tasks) >= 50 {
				break
			}
		}
	}
	u.render(w, http.StatusOK, "pipelines", pipelinesData{
		Base:    u.base(r, rp.Name+" 流水线"),
		Repo:    rp.FullName(),
		Tasks:   tasks,
		Running: u.Hist.Count(model.StatusRunning),
		Pending: u.Queue.Pending(),
		Msg:     r.URL.Query().Get("msg"),
	})
}

// mustRepoForTask 取任务对应仓库；仓库已删除时返回零值（调用方需另行处理）。
func (u *UI) mustRepoForTask(t model.Task) store.Repo {
	rp, _ := u.Store.GetRepo(t.Repo)
	return rp
}

// canSeeTask 判断用户能否查看某任务：仓库存在且用户可读；仓库已删除时
// 仅管理员可看历史记录。
func (u *UI) canSeeTask(usr store.User, t model.Task) bool {
	if usr.Admin {
		return true
	}
	rp, ok := u.Store.GetRepo(t.Repo)
	return ok && u.Store.CanRead(usr.Name, rp)
}

func (u *UI) pipelineDetail(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return
	}
	t, ok := u.Hist.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !u.canSeeTask(usr, t) {
		http.Error(w, "无权查看该任务", http.StatusForbidden)
		return
	}
	// Pass a pointer: template method calls like .Task.Duration need an
	// addressable value (pointer receiver).
	u.render(w, http.StatusOK, "pipeline_detail", &pipelineDetailData{
		Base:     u.base(r, "任务 "+t.ID),
		Task:     t,
		LogTail:  fsutil.TailFile(t.LogFile, 96*1024),
		CanWrite: u.Store.CanWrite(usr.Name, u.mustRepoForTask(t)),
	})
}

// taskLogAPI returns the live status and log tail of a task; the pipeline
// detail page polls this every 2s while the task is running. Same visibility
// rules as the detail page — the log body would otherwise leak to any
// logged-in user.
func (u *UI) taskLogAPI(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return
	}
	t, ok := u.Hist.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !u.canSeeTask(usr, t) {
		http.Error(w, "无权查看该任务", http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     t.ID,
		"status": t.Status,
		"stage":  t.Stage,
		"error":  t.Error,
		"log":    fsutil.TailFile(t.LogFile, 96*1024),
		"steps":  t.Steps,
	})
}
