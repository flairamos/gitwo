package gitserver

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gitwo/internal/store"
)

// 受保护主分支机制：组织仓库的主分支（DeployBranch，缺省 main）仅组织
// 创建者与管理员可推送/删除，其他成员只能推送到自己的分支。
// 实现方式与 Gitea/Gogs 一致：每个裸仓库安装 pre-receive 钩子，钩子回调
// `gitwo hook pre-receive` 子命令做规则校验；拒绝时以非零码退出，git 会把
// 钩子 stderr 回显给推送者。

const hookMarker = "# gitwo-managed-hook"

// pushIdentityEnv 生成 receive-pack 的身份环境（pre-receive 钩子经继承读取）。
func (g *Git) pushIdentityEnv(user, repo string) []string {
	env := []string{
		"GITWO_USER=" + user,
		"GITWO_REPO=" + repo,
	}
	if g.DataDir != "" {
		env = append(env, "GITWO_HOME="+filepath.Dir(g.DataDir))
	}
	return env
}

// EnsureHook 为单个裸仓库安装 pre-receive 钩子（幂等：gitwo 管理的钩子
// 每次启动刷新以更新二进制路径；用户自有钩子不覆盖）。
func (g *Git) EnsureHook(repoPath string) error {
	if g.SelfExe == "" {
		return nil
	}
	hooksDir := filepath.Join(repoPath, "hooks")
	hookPath := filepath.Join(hooksDir, "pre-receive")
	if data, err := os.ReadFile(hookPath); err == nil && !strings.Contains(string(data), hookMarker) {
		return nil // 用户自有钩子，不覆盖
	}
	script := "#!/bin/sh\n" + hookMarker + "\nexec \"" + g.SelfExe + "\" hook pre-receive\n"
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(hookPath, []byte(script), 0o755)
}

// EnsureAllHooks 遍历仓库根目录下所有裸仓库安装钩子（启动时执行，
// 兼容本功能之前创建的仓库）。
func (g *Git) EnsureAllHooks() {
	if g.SelfExe == "" {
		return
	}
	_ = filepath.WalkDir(g.ReposDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && strings.HasSuffix(p, ".git") {
			if err := g.EnsureHook(p); err != nil {
				g.Log.Warn("install hook", "repo", p, "err", err)
			}
			return filepath.SkipDir
		}
		return nil
	})
}

// PreReceiveUpdate 是钩子 stdin 的一行 "old new ref"。
type PreReceiveUpdate struct {
	Old, New, Ref string
}

func isZeroSHA(s string) bool { return strings.Trim(s, "0") == "" }

// CheckProtectedBranch 校验推送是否触碰受保护主分支。
// 规则：组织仓库（owner 为 org:*）的主分支仅组织创建者与管理员可推送/删除；
// 其他组织成员须推送到自己的分支。用户仓库（归属为个人）无需保护——
// 有写权限者即归属者本人。
func CheckProtectedBranch(st *store.Store, user, repoFull string, updates []PreReceiveUpdate) error {
	rp, ok := st.GetRepo(repoFull)
	if !ok {
		return nil // 仓库不存在/已删除：交由其他环节处理
	}
	if !strings.HasPrefix(rp.Owner, "org:") {
		return nil
	}
	usr, ok := st.GetUser(user)
	if !ok {
		return nil
	}
	if usr.Admin {
		return nil
	}
	_, orgName, _ := strings.Cut(rp.Owner, ":")
	if user == st.OrgCreator(orgName) {
		return nil
	}
	protected := rp.DeployBranch
	if protected == "" {
		protected = "main"
	}
	for _, u := range updates {
		if u.Ref != "refs/heads/"+protected {
			continue
		}
		action := "推送"
		if isZeroSHA(u.New) {
			action = "删除"
		}
		return fmt.Errorf(
			"push declined: 主分支 %s 受保护 —— 仅组织创建者(%s)与管理员可直接%s；请推送到自己的分支：git push origin HEAD:your-branch",
			protected, creatorOf(rp.Owner, st), action)
	}
	return nil
}

func creatorOf(owner string, st *store.Store) string {
	_, name, _ := strings.Cut(owner, ":")
	if c := st.OrgCreator(name); c != "" {
		return c
	}
	return "管理员"
}

// RunPreReceiveHook 在 pre-receive 钩子进程里执行（`gitwo hook pre-receive`）：
// stdin 为 "old new ref" 行；身份/仓库/数据目录来自父进程注入的环境。
// 返回进程退出码：0 放行，1 拒绝（stderr 内容会回显给推送者）。
// 环境缺失时 fail-open——推送者身份由 HTTP/SSH 认证层另行保证。
func RunPreReceiveHook(stdin io.Reader, stderr io.Writer, dataDir string) int {
	user := os.Getenv("GITWO_USER")
	repoFull := os.Getenv("GITWO_REPO")
	if user == "" || repoFull == "" {
		fmt.Fprintln(stderr, "warning: gitwo hook env missing (GITWO_USER/GITWO_REPO), protection check skipped")
		return 0
	}
	var updates []PreReceiveUpdate
	sc := bufio.NewScanner(stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 {
			continue
		}
		updates = append(updates, PreReceiveUpdate{Old: f[0], New: f[1], Ref: f[2]})
	}
	st, err := store.Load(dataDir)
	if err != nil {
		fmt.Fprintf(stderr, "warning: gitwo hook load store: %v, protection check skipped\n", err)
		return 0
	}
	if err := CheckProtectedBranch(st, user, repoFull, updates); err != nil {
		fmt.Fprintf(stderr, "\n============================================\n")
		fmt.Fprintf(stderr, "  %v\n", err)
		fmt.Fprintf(stderr, "============================================\n")
		return 1
	}
	return 0
}
