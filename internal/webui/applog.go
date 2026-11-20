package webui

// 运行日志（app.log）查看：服务在运行目录（run.workdir，缺省
// apps/<归属_仓库>/）里由 `nohup <run.command> >> app.log 2>&1` 启动，
// 因此 app.log 固定叫这个名字、位置由 run.workdir 决定（部署描述符
// .gitwo-site.json 的 workdir 字段记录它，旧部署回退到 apps/<slug>）。
//
// 访问控制：必须是登录的开发者/管理员，且对该仓库有读权限（测试角色
// 没有源码/流水线权限，也看不到运行日志）；按仓库隔离，不提供全局视图。
// 分页：日志可能很大（服务自己的 stdout/stderr 无限追加），因此按“从文件
// 末尾往前”的字节窗口分页：每页最多 200 行 / 256 KiB，游标是字节偏移，
// 单次读取成本与页大小成正比，与文件总大小无关。

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gitwo/internal/pipeline"
	"gitwo/internal/store"
)

const (
	appLogName      = "app.log"
	appLogPageLines = 200       // 默认每页行数
	appLogMaxLines  = 500       // 单页行数上限
	appLogPageBytes = 256 << 10 // 单页读取字节上限
	appLogLineMax   = 8 << 10   // 单行展示上限
)

// appLogPath 返回仓库服务运行目录里的 app.log 路径。
func (u *UI) appLogPath(repo string) string {
	appDir := u.Pipe.Params.AppDirFor(repo)
	if meta, ok := pipeline.SiteMeta(appDir); ok && meta.Workdir != "" {
		return filepath.Join(meta.Workdir, appLogName)
	}
	return filepath.Join(appDir, appLogName)
}

// appLogRepo 解析并鉴权：登录 + 非测试角色 + 对该仓库可读。
func (u *UI) appLogRepo(w http.ResponseWriter, r *http.Request) (store.Repo, store.User, bool) {
	usr, ok := u.requireDeveloper(w, r)
	if !ok {
		return store.Repo{}, store.User{}, false
	}
	owner, name := r.PathValue("owner"), r.PathValue("name")
	rp, ok := u.Store.GetRepo(owner + "/" + name)
	if !ok {
		http.NotFound(w, r)
		return store.Repo{}, store.User{}, false
	}
	if !u.Store.CanRead(usr.Name, rp) {
		http.Error(w, "无权查看该仓库的运行日志", http.StatusForbidden)
		return store.Repo{}, store.User{}, false
	}
	return rp, usr, true
}

// appLogPage 是一页日志（按时间正序，最后一行最新）。
type appLogPage struct {
	Lines   []string
	Start   int64 // 本页第一行的字节偏移
	End     int64 // 本页结束偏移（游标边界）
	HasMore bool  // 更早的内容是否存在
	Size    int64
}

// readAppLogPage 从 before 偏移向前读取至多 maxLines 行（最多 maxBytes 字节）。
// before <= 0 或超出文件大小时从文件末尾开始。返回的 Start 可作为下一页的
// before（即“加载更早”）。不依赖行号索引，单次成本 O(页大小)。
func readAppLogPage(path string, before int64, maxLines int, maxBytes int64) (appLogPage, error) {
	pg := appLogPage{}
	f, err := os.Open(path)
	if err != nil {
		return pg, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return pg, err
	}
	pg.Size = st.Size()
	end := st.Size()
	if before > 0 && before < end {
		end = before
	}
	if end < 0 {
		end = 0
	}
	pg.End = end

	var buf []byte
	pos := end
	for pos > 0 && int64(len(buf)) < maxBytes {
		n := int64(64 << 10)
		if n > pos {
			n = pos
		}
		if remain := maxBytes - int64(len(buf)); n > remain {
			n = remain
		}
		if n <= 0 {
			break
		}
		chunk := make([]byte, n)
		if _, rerr := f.ReadAt(chunk, pos-n); rerr != nil && !errors.Is(rerr, io.EOF) {
			return pg, rerr
		}
		buf = append(chunk, buf...)
		pos -= n
		if bytes.Count(buf, []byte{'\n'}) > maxLines {
			break
		}
	}
	// 窗口从文件中间开始时，第一个片段可能是不完整的行：丢掉它。
	if pos > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			pos += int64(i + 1)
			buf = buf[i+1:]
		}
	}
	lines := strings.Split(string(buf), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // 文件以换行结尾
	}
	if len(lines) > maxLines {
		drop := len(lines) - maxLines
		var cut int64
		for i := 0; i < drop; i++ {
			cut += int64(len(lines[i]) + 1) // 被丢弃的行都以 \n 结尾
		}
		pos += cut
		lines = lines[drop:]
	}
	out := make([]string, len(lines))
	for i, ln := range lines {
		ln = strings.TrimSuffix(ln, "\r")
		if len(ln) > appLogLineMax {
			ln = ln[:appLogLineMax] + "…(行过长已截断)"
		}
		out[i] = strings.ToValidUTF8(ln, "\uFFFD")
	}
	pg.Lines = out
	pg.Start = pos
	pg.HasMore = pos > 0
	return pg, nil
}

// apiURL 是该仓库日志 JSON 接口地址（供模板/JS 使用）。
func (u *UI) appLogAPIURL(repo string) string {
	return "/api/repos/" + repo + "/logs"
}

// repoLogsPage 是仓库运行日志页（服务端先渲染最新一页，JS 再支持“加载更早”）。
func (u *UI) repoLogsPage(w http.ResponseWriter, r *http.Request) {
	rp, user, ok := u.appLogRepo(w, r)
	if !ok {
		return
	}
	head := rp.DeployBranch
	if head == "" {
		if _, h, err := u.branches(rp.FullName()); err == nil {
			head = h
		}
	}
	path := u.appLogPath(rp.FullName())
	data := appLogData{
		Base:     u.base(r, rp.Name+" 运行日志"),
		RepoPage: u.repoHeader(rp, user.Name, head).WithTab("logs"),
		LogPath:  filepath.Join("apps", rp.Slug(), appLogName),
		APIURL:   u.appLogAPIURL(rp.FullName()),
	}
	pg, err := readAppLogPage(path, 0, appLogPageLines, appLogPageBytes)
	switch {
	case err == nil:
		data.Exists = true
		data.Lines = strings.Join(pg.Lines, "\n")
		data.Size = pg.Size
		data.Start = pg.Start
		data.HasMore = pg.HasMore
	case errors.Is(err, fs.ErrNotExist):
		data.Reason = "该仓库还没有 app.log：没有部署 service/dual 类型的服务，或服务尚未启动过。"
	default:
		data.Reason = "读取 app.log 失败：" + err.Error()
	}
	u.render(w, http.StatusOK, "applog", data)
}

// repoLogsAPI 返回 app.log 的分页 JSON：?before=<字节偏移>&lines=<行数>。
func (u *UI) repoLogsAPI(w http.ResponseWriter, r *http.Request) {
	rp, _, ok := u.appLogRepo(w, r)
	if !ok {
		return
	}
	var before int64
	if s := r.URL.Query().Get("before"); s != "" {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil && v > 0 {
			before = v
		}
	}
	maxLines := appLogPageLines
	if s := r.URL.Query().Get("lines"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			maxLines = v
		}
	}
	if maxLines < 1 {
		maxLines = 1
	}
	if maxLines > appLogMaxLines {
		maxLines = appLogMaxLines
	}
	pg, err := readAppLogPage(u.appLogPath(rp.FullName()), before, maxLines, appLogPageBytes)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{
				"exists": false,
				"reason": "该仓库还没有 app.log：没有部署 service/dual 类型的服务，或服务尚未启动过。",
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "读取 app.log 失败：" + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"exists":   true,
		"lines":    pg.Lines,
		"start":    pg.Start,
		"end":      pg.End,
		"has_more": pg.HasMore,
		"size":     pg.Size,
	})
}
