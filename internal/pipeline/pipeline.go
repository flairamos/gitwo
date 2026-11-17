// Package pipeline orchestrates one deployment task end to end:
// git sync -> parse deploy.yaml -> build -> publish (static target /
// binary install) -> service restart (nohup + pid file) -> liveness check,
// writing a task log file and a .gitwo-site.json descriptor that lets
// the web UI expose the deployed site under /sites/<repo>/.
package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitwo/internal/deploycfg"
	"gitwo/internal/fsutil"
	"gitwo/internal/gitutil"
	"gitwo/internal/history"
	"gitwo/internal/model"
	"gitwo/internal/procmgr"
	"gitwo/internal/rsyncutil"
	"gitwo/internal/shellutil"
	"gitwo/internal/store"
)

// Timeouts for individual pipeline steps (plain durations; parsed by appconfig).
type Timeouts struct {
	Git       time.Duration
	Build     time.Duration
	Deploy    time.Duration
	Stop      time.Duration
	StartWait time.Duration
}

// Params wires the pipeline to the integrated platform's layout.
type Params struct {
	ReposRoot     string // bare repositories (<home>/repos) — clone source
	WorkspaceRoot string // build checkouts (<home>/workspaces)
	AppsRoot      string // deployed services & static targets (<home>/apps)
	NotifyWebhook string // 部署结束通知的 webhook URL（空禁用）
	LogRoot       string // task logs (/tmp/gitwo/logs)
	TmpRoot       string // build scratch base (/tmp/gitwo/tmp)

	PortMin int // auto-assign range for services without run.port
	PortMax int

	Timeouts           Timeouts
	DefaultVerifyIndex bool
}

// repo full names are "owner/name"; filesystem paths use the slug form
// "owner_name" so every derived directory stays flat and unambiguous.

func RepoSlug(full string) string { return strings.ReplaceAll(full, "/", "_") }

func (p Params) RepoPath(full string) string {
	owner, name, ok := store.SplitFullName(full)
	if !ok {
		owner, name = "legacy", full
	}
	return filepath.Join(p.ReposRoot, owner, name+".git")
}
func (p Params) WorkdirFor(full string) string { return filepath.Join(p.WorkspaceRoot, RepoSlug(full)) }
func (p Params) AppDirFor(full string) string  { return filepath.Join(p.AppsRoot, RepoSlug(full)) }

type Pipeline struct {
	Params  Params
	Hist    *history.Store
	Log     *slog.Logger
	SelfExe string
}

// NewTask builds a task ready to be queued (id + log path assigned).
func (p *Pipeline) NewTask(repo, branch, sha string) *model.Task {
	t := &model.Task{
		ID:        model.NewID(),
		Repo:      repo,
		Branch:    branch,
		SHA:       sha,
		CloneURL:  p.Params.RepoPath(repo),
		Workdir:   p.Params.WorkdirFor(repo),
		Status:    model.StatusPending,
		CreatedAt: time.Now(),
	}
	t.LogFile = filepath.Join(p.Params.LogRoot, t.Slug(), t.ID+".log")
	return t
}

// Execute runs the full pipeline for a task. It is invoked by the queue and
// never panics into the caller.
func (p *Pipeline) Execute(t *model.Task) {
	t.Status = model.StatusRunning
	t.StartedAt = time.Now()
	p.Log.Info("deploy started", "task", t.ID, "repo", t.Repo, "branch", t.Branch, "sha", t.ShortSHA())

	err := p.run(t)

	// 构建临时目录用后即删（/tmp/gitwo/tmp/<task>）
	_ = os.RemoveAll(filepath.Join(p.Params.TmpRoot, t.ID))
	// 任务日志按仓库只保留最近 keepTaskLogs 份，防止垃圾数据累积
	p.pruneLogs(t.Repo)

	t.FinishedAt = time.Now()
	dur := t.Duration().Round(time.Second)
	if err != nil {
		t.Status = model.StatusFailed
		t.Error = err.Error()
		p.Log.Error("deploy failed", "task", t.ID, "repo", t.Repo, "branch", t.Branch,
			"stage", t.Stage, "error", err, "duration", dur.String())
	} else {
		t.Status = model.StatusSuccess
		p.Log.Info("deploy succeeded", "task", t.ID, "repo", t.Repo, "branch", t.Branch,
			"sha", t.ShortSHA(), "duration", dur.String())
	}
	p.Hist.Persist(t)
	p.notifyWebhook(t)
}

// notifyWebhook 部署结束后向配置的 webhook URL POST 结果摘要
// （server.yaml 的 notify_webhook；空则跳过）。异步、失败仅记日志。
func (p *Pipeline) notifyWebhook(t *model.Task) {
	if p.Params.NotifyWebhook == "" {
		return
	}
	if t.Status != model.StatusSuccess && t.Status != model.StatusFailed {
		return
	}
	payload, err := json.Marshal(map[string]any{
		"event":       "deploy.finished",
		"task_id":     t.ID,
		"repo":        t.Repo,
		"branch":      t.Branch,
		"commit":      t.SHA,
		"status":      t.Status,
		"duration_ms": t.Duration().Milliseconds(),
		"error":       t.Error,
	})
	if err != nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Params.NotifyWebhook, bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitwo-Event", "deploy.finished")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			p.Log.Warn("notify webhook", "task", t.ID, "err", err)
			return
		}
		resp.Body.Close()
		p.Log.Info("notify webhook sent", "task", t.ID, "status", t.Status)
	}()
}

func (p *Pipeline) run(t *model.Task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("gitwo internal error: %v", r)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(t.LogFile), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	lf, err := os.Create(t.LogFile)
	if err != nil {
		return fmt.Errorf("open task log %s: %w", t.LogFile, err)
	}
	defer lf.Close()
	w := io.Writer(lf)

	fmt.Fprintf(w, "gitwo task %s\nrepo=%s branch=%s commit=%s\nworkdir=%s\nhost=%s\nstart=%s\n\n",
		t.ID, t.Repo, t.Branch, t.SHA, t.Workdir, hostname(), t.StartedAt.Local().Format(time.RFC3339))

	var dc *deploycfg.Deploy

	if err := p.step(t, w, "git-sync", func(w io.Writer) error {
		ctx, cancel := context.WithTimeout(context.Background(), p.Params.Timeouts.Git)
		defer cancel()
		sha, err := gitutil.Sync(ctx, t.CloneURL, t.Workdir, t.Branch, t.SHA, p.envFor(t), w)
		if err != nil {
			return err
		}
		if t.SHA == "" { // manual deploy without -sha
			t.SHA = sha
		}
		return nil
	}); err != nil {
		return err
	}

	if err := p.step(t, w, "load-config", func(w io.Writer) error {
		var e error
		dc, e = deploycfg.LoadWorkspace(t.Workdir)
		if e != nil {
			return fmt.Errorf("%w（在仓库根目录添加 deploy.yaml，或在仓库设置中指定部署分支）", e)
		}
		return dc.Validate(deploycfg.Defaults{
			Workdir:      p.Params.AppDirFor(t.Repo),
			StaticTarget: filepath.Join(p.Params.AppDirFor(t.Repo), "static"),
		}, p.Params.DefaultVerifyIndex)
	}); err != nil {
		return err
	}

	envBuild := p.envFor(t, dc.Env)
	site := SiteInfo{}

	switch dc.Type {
	case deploycfg.TypeStatic:
		// Zero-config plain static sites: no build.frontend section at all.
		// The repo root itself is the site (index.html must exist); .git and
		// deploy.yaml are excluded from publishing automatically.
		if dc.Build.Frontend == nil {
			if err := p.step(t, w, "detect-frontend", func(w io.Writer) error {
				if _, e := os.Stat(filepath.Join(t.Workdir, "index.html")); e != nil {
					return fmt.Errorf("仓库根目录没有 index.html：" +
						"原生前端请把静态文件放在仓库根目录；" +
						"Vite/Vue 等需要构建的项目请配置 build.frontend（command + output）")
				}
				dc.Build.Frontend = &deploycfg.Frontend{
					Output: ".",
					Target: filepath.Join(p.Params.AppDirFor(t.Repo), "static"),
				}
				fmt.Fprintln(w, "检测到原生静态站点（根目录 index.html）：跳过构建，直接发布仓库根目录")
				return nil
			}); err != nil {
				return err
			}
		}
		var src string
		if err := p.step(t, w, "build-frontend", func(w io.Writer) error {
			var e error
			src, e = p.buildFrontend(t, w, dc, envBuild)
			return e
		}); err != nil {
			return err
		}
		if err := p.step(t, w, "deploy-frontend", func(w io.Writer) error {
			return p.deployFrontend(t, w, dc.Build.Frontend, src)
		}); err != nil {
			return err
		}
		site.StaticDir = dc.Build.Frontend.Target

	case deploycfg.TypeService:
		if dc.Build.Frontend != nil {
			var src string
			if err := p.step(t, w, "build-frontend", func(w io.Writer) error {
				var e error
				src, e = p.buildFrontend(t, w, dc, envBuild)
				return e
			}); err != nil {
				return err
			}
			if err := p.step(t, w, "deploy-frontend", func(w io.Writer) error {
				return p.deployFrontend(t, w, dc.Build.Frontend, src)
			}); err != nil {
				return err
			}
			site.StaticDir = dc.Build.Frontend.Target
		}
		port, err := p.buildAndRestart(t, w, dc, envBuild)
		if err != nil {
			return err
		}
		site.Port = port

	case deploycfg.TypeDual:
		var src string
		if err := p.step(t, w, "build-frontend", func(w io.Writer) error {
			var e error
			src, e = p.buildFrontend(t, w, dc, envBuild)
			return e
		}); err != nil {
			return err
		}
		if err := p.step(t, w, "deploy-frontend", func(w io.Writer) error {
			return p.deployFrontend(t, w, dc.Build.Frontend, src)
		}); err != nil {
			return err
		}
		site.StaticDir = dc.Build.Frontend.Target
		port, err := p.buildAndRestart(t, w, dc, envBuild)
		if err != nil {
			return err
		}
		site.Port = port
	}

	if err := p.step(t, w, "publish-site", func(w io.Writer) error {
		if dc.StaticServer != nil {
			site.SPA = dc.StaticServer.SPAServe
		}
		return writeSiteMeta(p.Params.AppDirFor(t.Repo), site)
	}); err != nil {
		return err
	}

	fmt.Fprintf(w, "\n====> deploy finished successfully in %s\n", t.Duration().Round(time.Millisecond))
	return nil
}

// buildAndRestart runs the backend half shared by service and dual modes:
// build -> install (backup + atomic swap) -> restart via nohup with $PORT.
// Returns the resolved listen port.
func (p *Pipeline) buildAndRestart(t *model.Task, w io.Writer, dc *deploycfg.Deploy, envBuild []string) (int, error) {
	var bin string
	if err := p.step(t, w, "build-backend", func(w io.Writer) error {
		var e error
		bin, e = p.buildBackend(t, w, dc, envBuild)
		return e
	}); err != nil {
		return 0, err
	}
	if err := p.step(t, w, "release-binary", func(w io.Writer) error {
		return p.releaseBackend(w, dc, bin)
	}); err != nil {
		return 0, err
	}
	if len(dc.Run.Files) > 0 {
		if err := p.step(t, w, "sync-run-files", func(w io.Writer) error {
			return p.syncRunFiles(t, w, dc)
		}); err != nil {
			return 0, err
		}
	}
	var port int
	if err := p.step(t, w, "restart-service", func(w io.Writer) error {
		var e error
		port, e = p.restartBackend(t, w, dc)
		return e
	}); err != nil {
		return 0, err
	}
	return port, nil
}

// step runs one pipeline stage, records timing into the task and appends a
// human-readable block to the task log.
func (p *Pipeline) step(t *model.Task, w io.Writer, name string, fn func(io.Writer) error) error {
	start := time.Now()
	t.Stage = name
	fmt.Fprintf(w, "\n====> [%s] %s\n", start.Local().Format("15:04:05"), name)
	err := fn(w)
	sr := model.Step{Name: name, Start: start, End: time.Now(), OK: err == nil}
	if err != nil {
		sr.Detail = err.Error()
		fmt.Fprintf(w, "====> [%s] FAILED in %s: %v\n", name, sr.End.Sub(start).Round(time.Millisecond), err)
	} else {
		fmt.Fprintf(w, "====> [%s] done in %s\n", name, sr.End.Sub(start).Round(time.Millisecond))
	}
	t.Steps = append(t.Steps, sr)
	return err
}

func (p *Pipeline) envFor(t *model.Task, layers ...map[string]string) []string {
	all := append(layers, map[string]string{
		"GITWO_REPO":   t.Repo,
		"GITWO_BRANCH": t.Branch,
		"GITWO_COMMIT": t.SHA,
		"GITWO_TASK":   t.ID,
		"TMPDIR":       filepath.Join(p.Params.TmpRoot, t.ID), // build temp under /tmp
	})
	_ = os.MkdirAll(filepath.Join(p.Params.TmpRoot, t.ID), 0o755)
	return shellutil.EnvLayer(all...)
}

func (p *Pipeline) buildFrontend(t *model.Task, w io.Writer, dc *deploycfg.Deploy, env []string) (string, error) {
	f := dc.Build.Frontend
	dir := t.Workdir
	if f.Dir != "" {
		dir = filepath.Join(t.Workdir, f.Dir)
	}
	if strings.TrimSpace(f.Command) == "" {
		// 原生静态目录：无需构建，直接发布 output 指向的目录
		fmt.Fprintln(w, "build.frontend.command 为空：原生静态站点，跳过构建，直接发布该目录")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), p.Params.Timeouts.Build)
		defer cancel()
		if err := shellutil.Shell(ctx, dir, env, w, f.Command); err != nil {
			return "", fmt.Errorf("frontend build failed: %w", err)
		}
	}
	out := f.Output
	if !filepath.IsAbs(out) {
		out = filepath.Join(t.Workdir, out)
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", fmt.Errorf("frontend output %s not found (check build.frontend.output；原生站点请确认该目录存在)", f.Output)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("frontend output %s is not a directory", f.Output)
	}
	return out, nil
}

func (p *Pipeline) deployFrontend(t *model.Task, w io.Writer, f *deploycfg.Frontend, src string) error {
	// Refuse to publish into (or under) the git workspace itself: rsync
	// --delete against the workspace would destroy the working copy.
	if rel, rerr := filepath.Rel(t.Workdir, f.Target); rerr == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("build.frontend.target %s 位于仓库工作区内，rsync --delete 会破坏工作区；请改用缺省的 apps 目录或仓库外路径", f.Target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.Params.Timeouts.Deploy)
	defer cancel()
	if err := rsyncutil.Sync(ctx, src, f.Target, w); err != nil {
		return fmt.Errorf("frontend deploy failed: %w", err)
	}
	if f.ShouldVerifyIndex() {
		if err := rsyncutil.VerifyIndex(f.Target); err != nil {
			return fmt.Errorf("frontend deploy check failed: %w", err)
		}
	}
	return nil
}

func (p *Pipeline) buildBackend(t *model.Task, w io.Writer, dc *deploycfg.Deploy, env []string) (string, error) {
	b := dc.Build.Backend
	dir := t.Workdir
	if b.Dir != "" {
		dir = filepath.Join(t.Workdir, b.Dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.Params.Timeouts.Build)
	defer cancel()
	if err := shellutil.Shell(ctx, dir, env, w, b.Command); err != nil {
		return "", fmt.Errorf("backend build failed: %w", err)
	}
	out := b.Output
	if !filepath.IsAbs(out) {
		out = filepath.Join(t.Workdir, out)
	}
	st, err := os.Stat(out)
	if err != nil {
		return "", fmt.Errorf("backend binary %s not found after build (check build.backend.output)", b.Output)
	}
	if st.IsDir() {
		return "", fmt.Errorf("backend binary %s is a directory", b.Output)
	}
	return out, nil
}

// releaseBackend installs the freshly built binary into the service workdir:
// backup the old one (optional), then an atomic temp+rename copy that never
// trips "text file busy" on a running binary.
func (p *Pipeline) releaseBackend(w io.Writer, dc *deploycfg.Deploy, binPath string) error {
	run := dc.Run
	if err := os.MkdirAll(run.Workdir, 0o755); err != nil {
		return fmt.Errorf("create workdir %s: %w", run.Workdir, err)
	}
	dst := filepath.Join(run.Workdir, filepath.Base(dc.Build.Backend.Output))
	if err := procmgr.BackupBinary(dst, run.KeepBinaries, w); err != nil {
		return fmt.Errorf("backup old binary: %w", err)
	}
	if err := procmgr.CopyBinary(binPath, dst); err != nil {
		return fmt.Errorf("install binary to %s: %w", dst, err)
	}
	fmt.Fprintf(w, "installed %s -> %s\n", binPath, dst)
	return nil
}

// syncRunFiles publishes run.files (source files needed at runtime — python
// scripts, node entrypoints, config files …) from the workspace into the
// service workdir. Single files are copied, directories are mirrored.
func (p *Pipeline) syncRunFiles(t *model.Task, w io.Writer, dc *deploycfg.Deploy) error {
	run := dc.Run
	if err := os.MkdirAll(run.Workdir, 0o755); err != nil {
		return fmt.Errorf("create workdir %s: %w", run.Workdir, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.Params.Timeouts.Deploy)
	defer cancel()
	for _, rel := range run.Files {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		src := filepath.Join(t.Workdir, rel)
		st, err := os.Stat(src)
		if err != nil {
			return fmt.Errorf("run.files entry %q not found in repo (check run.files)", rel)
		}
		if st.IsDir() {
			if err := rsyncutil.Sync(ctx, src, filepath.Join(run.Workdir, rel), w); err != nil {
				return fmt.Errorf("sync %s: %w", rel, err)
			}
			continue
		}
		dst := filepath.Join(run.Workdir, filepath.Base(rel))
		fmt.Fprintf(w, "$ cp %s -> %s\n", src, dst)
		if err := rsyncutil.CopyFile(src, dst); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
	fmt.Fprintf(w, "run.files synced: %s\n", strings.Join(run.Files, ", "))
	return nil
}

// resolveServicePort picks the listen port for a service: explicit
// run.port > previous assignment (stable across deploys) > first free port
// in the configured range (seeded by repo name for stable ordering).
func (p *Pipeline) resolveServicePort(repo string, dc *deploycfg.Deploy) (int, error) {
	if dc.Run.Port > 0 {
		return dc.Run.Port, nil
	}
	if prev, err := readSiteMeta(p.Params.AppDirFor(repo)); err == nil && prev.Port > 0 {
		if procmgr.CheckPort(prev.Port) == nil {
			return prev.Port, nil
		}
	}
	span := p.Params.PortMax - p.Params.PortMin + 1
	start := 0
	h := fnv.New32a()
	if _, err := h.Write([]byte(repo)); err == nil {
		start = int(h.Sum32()) % span
	}
	for i := 0; i < span; i++ {
		port := p.Params.PortMin + (start+i)%span
		if procmgr.CheckPort(port) == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port available in range %d-%d", p.Params.PortMin, p.Params.PortMax)
}

// restartBackend stops the old process (pid file + PID-reuse validation),
// resolves the listen port (after the old process is stopped, so the
// previous port is seen as free and stays stable across deploys), starts
// the new process via nohup with $PORT injected, then confirms liveness.
func (p *Pipeline) restartBackend(t *model.Task, w io.Writer, dc *deploycfg.Deploy) (int, error) {
	run := dc.Run
	expect := procmgr.ExpectBinary(run.Command)
	if expect == "" {
		return 0, fmt.Errorf("run.command is empty")
	}
	stopped, err := procmgr.Stop(run.Workdir, expect, w, p.Params.Timeouts.Stop)
	if err != nil {
		return 0, fmt.Errorf("stop old service: %w", err)
	}
	if !stopped {
		fmt.Fprintln(w, "no old service was running")
	}
	port, err := p.resolveServicePort(t.Repo, dc)
	if err != nil {
		return 0, err
	}
	if port > 0 {
		if err := procmgr.CheckPort(port); err != nil {
			return 0, fmt.Errorf("restart aborted: %w", err)
		}
	}
	env := p.envFor(t, dc.Env, run.Env, map[string]string{
		"PORT":       strconv.Itoa(port),
		"GITWO_PORT": strconv.Itoa(port),
	})
	pid, err := procmgr.Start(run.Workdir, run.Command, env, w)
	if err != nil {
		return 0, fmt.Errorf("start service: %w", err)
	}
	if !procmgr.WaitAlive(pid, p.Params.Timeouts.StartWait) {
		fmt.Fprintln(w, "\nservice died right after start; last lines of app.log:")
		fmt.Fprintln(w, fsutil.TailFile(filepath.Join(run.Workdir, "app.log"), 2048))
		return 0, fmt.Errorf("service exited immediately after start (pid %d), see app.log in %s", pid, run.Workdir)
	}
	// Record the settled process name (after exec chains finished) so later
	// Stops validate the pid even when run.command is a wrapper (npm start…).
	procmgr.RecordName(run.Workdir, pid)
	if name, nerr := procmgr.ProcessName(pid); nerr == nil {
		fmt.Fprintf(w, "process %d runs as %q (recorded to %s)\n", pid, name, procmgr.NameFileName)
	}
	fmt.Fprintf(w, "service started and alive (pid %d, port %d)\n", pid, port)
	return port, nil
}

// SiteInfo describes the deployed site; written to
// apps/<repo>/.gitwo-site.json and read by the web UI /sites/<repo>/.
// SPA uses a pointer: absent = default true (SPA fallback on).
// SiteInfo 描述已部署站点的元数据（供 Web UI 的 /sites/ 托管与 /api/ 代理使用）。
type SiteInfo struct {
	StaticDir string `json:"static_dir,omitempty"`
	Port      int    `json:"port,omitempty"`
	SPA       *bool  `json:"spa,omitempty"`
	UpdatedAt string `json:"updated_at"`
}

func writeSiteMeta(appDir string, m SiteInfo) error {
	m.UpdatedAt = time.Now().Format(time.RFC3339)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(appDir, ".gitwo-site.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func readSiteMeta(appDir string) (SiteInfo, error) {
	var m SiteInfo
	data, err := os.ReadFile(filepath.Join(appDir, ".gitwo-site.json"))
	if err != nil {
		// 改名前的旧描述符（deployer 时代），兼容读取直到下次部署覆盖写入
		data, err = os.ReadFile(filepath.Join(appDir, ".deployer-site.json"))
	}
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(data, &m)
	return m, err
}

// SiteMeta exposes the site descriptor to the web UI.
func SiteMeta(appDir string) (SiteInfo, bool) {
	m, err := readSiteMeta(appDir)
	return m, err == nil
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// keepTaskLogs is how many most-recent task logs are retained per repo.
const keepTaskLogs = 20

// pruneLogs removes old task log files (task ids are timestamped, so file
// names sort chronologically), keeping the newest keepTaskLogs per repo.
func (p *Pipeline) pruneLogs(repo string) {
	dir := filepath.Join(p.Params.LogRoot, RepoSlug(repo))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var logs []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".log") {
			logs = append(logs, e.Name())
		}
	}
	sort.Strings(logs) // timestamps sort chronologically
	for i := 0; i < len(logs)-keepTaskLogs; i++ {
		_ = os.Remove(filepath.Join(dir, logs[i]))
	}
}

// PruneAllLogs runs pruneLogs for every repo's log dir (boot time).
func (p *Pipeline) PruneAllLogs() {
	dirs, err := os.ReadDir(p.Params.LogRoot)
	if err != nil {
		return
	}
	for _, d := range dirs {
		if d.IsDir() {
			p.pruneLogs(strings.ReplaceAll(d.Name(), "_", "/"))
		}
	}
}
