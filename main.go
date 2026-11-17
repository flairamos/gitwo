// Command gitwo is a self-contained mini git + CI/CD platform:
// builtin git server (clone/push over HTTP), web UI with users/groups,
// push-triggered build pipeline (deploy.yaml) and /sites/<repo>/ hosting
// for the deployed frontends. See README.md.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gitwo/internal/appconfig"
	"gitwo/internal/gitserver"
	"gitwo/internal/history"
	"gitwo/internal/model"
	"gitwo/internal/pipeline"
	"gitwo/internal/queue"
	"gitwo/internal/sshserver"
	"gitwo/internal/staticserver"
	"gitwo/internal/store"
	"gitwo/internal/version"
	"gitwo/internal/webui"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "version":
			fmt.Println("gitwo " + version.Version)
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "deploy":
			runDeploy(os.Args[2:])
			return
		case "hook":
			os.Exit(runHook(os.Args[2:]))
			return
		case "serve-static":
			runStatic(os.Args[2:])
			return
		case "help", "-h", "--help":
			usage()
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}
	runServe(nil)
}

func usage() {
	fmt.Print(`gitwo — self-contained git server + push-to-deploy platform

Usage:
  gitwo serve [-home <dir>]              run the platform on :9527 (default)
  gitwo deploy -repo <name> [-branch main] [-sha <sha>] [-home <dir>]
                                            trigger a deployment manually
  gitwo serve-static -addr :3000 -dir <dir> [-spa]
                                            utility static file server
  gitwo version

Home layout (default: the directory of the gitwo binary, override with
-home or GITWO_HOME):  repos/ workspaces/ apps/ data/  — temporary build
files and task logs go to /tmp/gitwo/. See README.md.
`)
}

// boot loads config, storage and the pipeline wiring shared by subcommands.
func boot(homeDir string) (*appconfig.Config, appconfig.Home, *store.Store, *pipeline.Pipeline, *history.Store, error) {
	home := appconfig.Resolve(homeDir)
	if err := home.EnsureDirs(); err != nil {
		return nil, home, nil, nil, nil, err
	}
	cfg, err := home.LoadConfig()
	if err != nil {
		return nil, home, nil, nil, nil, err
	}
	st, err := store.Load(home.DataDir())
	if err != nil {
		return nil, home, nil, nil, nil, fmt.Errorf("load data: %w", err)
	}
	migrateLayout(home, st)
	hist, err := history.New(200, filepath.Join(home.DataDir(), "history.jsonl"))
	if err != nil {
		return nil, home, nil, nil, nil, fmt.Errorf("load history: %w", err)
	}
	pipe := &pipeline.Pipeline{
		Params: pipeline.Params{
			ReposRoot:     home.ReposDir(),
			WorkspaceRoot: home.WorkspaceRoot(),
			AppsRoot:      home.AppsRoot(),
			LogRoot:       appconfig.LogRoot(),
			TmpRoot:       appconfig.TmpRoot(),
			PortMin:       cfg.PortMin,
			PortMax:       cfg.PortMax,
			Timeouts: pipeline.Timeouts{
				Git:       cfg.Timeouts.Git.D(),
				Build:     cfg.Timeouts.Build.D(),
				Deploy:    cfg.Timeouts.Deploy.D(),
				Stop:      cfg.Timeouts.Stop.D(),
				StartWait: cfg.Timeouts.StartWait.D(),
			},
			DefaultVerifyIndex: cfg.DefaultVerifyIndex(),
			NotifyWebhook:      cfg.NotifyWebhook,
		},
		Hist:    hist,
		Log:     slog.Default(),
		SelfExe: selfExe(),
	}
	pipe.PruneAllLogs() // 启动时清理各仓库超量的旧任务日志
	return cfg, home, st, pipe, hist, nil
}

// migrateLayout 把旧版扁平布局（repos/<name>.git / workspaces/<name> /
// apps/<name>）迁移为 owner 命名空间布局（repos/<owner>/<name>.git、
// workspaces/<owner>_<name>、apps/<owner>_<name>），并同步 site.json 里的
// 旧绝对路径。幂等：已迁移的目录自动跳过。
func migrateLayout(home appconfig.Home, st *store.Store) {
	dirExists := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	}
	for _, r := range st.ListRepos() {
		owner, slug := r.OwnerName(), r.Slug()

		old := filepath.Join(home.ReposDir(), r.Name+".git")
		new := filepath.Join(home.ReposDir(), owner, r.Name+".git")
		if old != new && dirExists(old) && !dirExists(new) {
			if err := os.MkdirAll(filepath.Dir(new), 0o755); err == nil {
				if err := os.Rename(old, new); err == nil {
					slog.Info("migrated repo dir", "repo", r.FullName())
				}
			}
		}

		for _, pair := range [][2]string{
			{filepath.Join(home.WorkspaceRoot(), r.Name), filepath.Join(home.WorkspaceRoot(), slug)},
			{filepath.Join(home.AppsRoot(), r.Name), filepath.Join(home.AppsRoot(), slug)},
		} {
			if pair[0] != pair[1] && dirExists(pair[0]) && !dirExists(pair[1]) {
				if err := os.Rename(pair[0], pair[1]); err == nil {
					slog.Info("migrated data dir", "dir", pair[1])
				}
			}
		}

		// site.json 的 static_dir 是绝对路径，同步改写前缀
		oldApp := filepath.Join(home.AppsRoot(), r.Name)
		newApp := filepath.Join(home.AppsRoot(), slug)
		if oldApp != newApp {
			siteFile := filepath.Join(newApp, ".gitwo-site.json")
			if data, err := os.ReadFile(siteFile); err == nil && strings.Contains(string(data), oldApp) {
				_ = os.WriteFile(siteFile, bytes.ReplaceAll(data, []byte(oldApp), []byte(newApp)), 0o644)
				slog.Info("migrated site paths", "repo", r.FullName())
			}
		}

		// 改名迁移：deployer 时代的 .deployer-site.json 改名为 .gitwo-site.json
		appDir := filepath.Join(home.AppsRoot(), slug)
		legacySite := filepath.Join(appDir, ".deployer-site.json")
		newSite := filepath.Join(appDir, ".gitwo-site.json")
		if fileExists(legacySite) && !fileExists(newSite) {
			if err := os.Rename(legacySite, newSite); err == nil {
				slog.Info("migrated site descriptor", "repo", r.FullName())
			}
		}
	}
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func selfExe() string {
	p, err := os.Executable()
	if err != nil {
		return os.Args[0]
	}
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	home := fs.String("home", "", "home directory (default: binary dir or $GITWO_HOME)")
	_ = fs.Parse(args)

	cfg, h, st, pipe, hist, err := boot(*home)
	if err != nil {
		slog.Error("init failed", "err", err)
		os.Exit(1)
	}
	q := queue.New(cfg.Workers, cfg.MaxQueued, pipe.Execute)
	ui := webui.New(cfg, h, st, pipe, hist, q, slog.Default())

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           ui.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	// first-run banner
	if len(st.ListUsers()) == 1 {
		if u, ok := st.GetUser("admin"); ok && u.Admin {
			slog.Warn("first run: default account created — admin / admin, change it at /me immediately")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ui.Git.SelfExe = selfExe()
	ui.Git.DataDir = h.DataDir()
	ui.Git.EnsureAllHooks() // 兼容既有仓库：启动时刷新 pre-receive 钩子

	if sshAddr := cfg.SSHAddr(); sshAddr != "" {
		sshSrv := &sshserver.Server{
			Addr:     sshAddr,
			ReposDir: h.ReposDir(),
			Store:    st,
			Log:      slog.Default(),
			OnPush:   ui.OnPush,
			DataDir:  h.DataDir(),
		}
		if err := sshSrv.EnsureHostKey(filepath.Join(h.DataDir(), "ssh")); err != nil {
			slog.Error("ssh host key init failed", "err", err)
			os.Exit(1)
		}
		go func() {
			if err := sshSrv.ListenAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
				slog.Error("ssh server failed", "err", err)
			}
		}()
		defer sshSrv.Close() // 停机时释放 SSH 监听
		slog.Info("ssh git server listening", "addr", sshAddr,
			"url", "ssh://git@<host>"+cfg.SSHSuffix()+"/<owner>/<repo>.git")
	}
	go func() {
		slog.Info("gitwo listening", "addr", cfg.Listen, "version", version.Version,
			"home", h.Dir, "repos", len(st.ListRepos()), "users", len(st.ListUsers()))
		slog.Info("git remote URL format", "url", "http://<host>"+cfg.ListenSuffix()+"/git/<owner>/<repo>.git  |  ssh://git@<host>"+cfg.SSHSuffix()+"/<owner>/<repo>.git")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down: draining running deployments (max 60s)")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if hist.Count(model.StatusRunning) == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	slog.Info("gitwo stopped")
}

// runDeploy triggers a deployment manually from the command line.
func runDeploy(args []string) {
	fs := flag.NewFlagSet("deploy", flag.ExitOnError)
	home := fs.String("home", "", "home directory")
	repo := fs.String("repo", "", "repository name")
	branch := fs.String("branch", "", "branch (default: repo deploy branch or repo default branch)")
	sha := fs.String("sha", "", "commit sha (default: branch head)")
	_ = fs.Parse(args)

	if *repo == "" {
		fmt.Fprintln(os.Stderr, "-repo is required")
		os.Exit(2)
	}
	_, homeDir, st, pipe, hist, err := boot(*home)
	if err != nil {
		slog.Error("init failed", "err", err)
		os.Exit(1)
	}
	rp, ok := st.GetRepo(*repo)
	if !ok {
		slog.Error("repo not registered", "repo", *repo)
		os.Exit(1)
	}
	if *branch == "" {
		*branch = rp.DeployBranch
	}
	if *sha == "" {
		out, err := gitRevParse(homeDir.ReposDir(), *repo, "refs/heads/"+*branch)
		if err != nil {
			slog.Error("cannot resolve branch head", "branch", *branch, "err", err)
			os.Exit(1)
		}
		*sha = out
	}

	task := pipe.NewTask(*repo, *branch, *sha)
	hist.Add(task)
	pipe.Execute(task)
	fmt.Printf("\ntask %s: %s\nlog: %s\n", task.ID, task.Status, task.LogFile)
	if task.Status != model.StatusSuccess {
		os.Exit(1)
	}
}

// gitRevParse resolves a ref in a bare repository under reposDir.
func gitRevParse(reposDir, repo, ref string) (string, error) {
	out, err := exec.Command("git", "--git-dir", filepath.Join(reposDir, repo+".git"),
		"rev-parse", ref).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// runHook 执行 pre-receive 钩子的保护分支校验（由裸仓库钩子回调，
// 身份/仓库/数据目录经 GITWO_USER/GITWO_REPO/GITWO_HOME 环境注入）。
func runHook(args []string) int {
	if len(args) < 1 || args[0] != "pre-receive" {
		fmt.Fprintln(os.Stderr, "unknown hook:", strings.Join(args, " "))
		return 1
	}
	home := appconfig.Resolve("")
	return gitserver.RunPreReceiveHook(os.Stdin, os.Stderr, home.DataDir())
}

func runStatic(args []string) {
	fs := flag.NewFlagSet("serve-static", flag.ExitOnError)
	addr := fs.String("addr", ":3000", "listen address")
	dir := fs.String("dir", "", "static dir to serve")
	spa := fs.Bool("spa", true, "fallback unknown paths to index.html")
	_ = fs.Parse(args)
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "-dir is required")
		os.Exit(2)
	}
	if err := staticserver.Serve(*addr, *dir, *spa); err != nil {
		slog.Error("static server exited", "err", err)
		os.Exit(1)
	}
}
