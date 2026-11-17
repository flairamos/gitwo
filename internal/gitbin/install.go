package gitbin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// installTimeout 给包管理器安装留足时间（下载 + 安装）。
const installTimeout = 10 * time.Minute

// install 是平台安装入口，测试可替换。
var install = platformInstall

// Ensure 确保 git 可用：先按 Resolve 的优先级解析；仍未找到且允许自动安装时，
// 尝试用系统包管理器安装（Windows: winget，Ubuntu: apt-get，CentOS: yum/dnf）。
// 安装失败（无网络/无权限/无包管理器）返回带手动安装指引的错误，调用方应
// 拒绝启动服务。
func Ensure(explicit string, autoInstall bool) error {
	if err := resolveOnce(explicit); err == nil {
		return nil
	}
	if !autoInstall {
		return fmt.Errorf("%s", manualHint("自动安装已禁用（auto_install_git: false）"))
	}
	slog.Warn("git not found — attempting automatic installation", "os", runtime.GOOS)
	start := time.Now()
	if err := install(); err != nil {
		return fmt.Errorf("自动安装 git 失败（可能无网络或无管理员权限）: %v。%s", err, manualHint(""))
	}
	slog.Info("git installation finished", "took", time.Since(start).Round(time.Second).String())
	reset()
	if err := resolveOnce(explicit); err != nil {
		return fmt.Errorf("git 安装命令已执行，但仍未找到可用的 git。%s", manualHint(""))
	}
	slog.Info("git resolved after install", "path", Path())
	return nil
}

// manualHint 生成按平台的手动安装指引。
func manualHint(reason string) string {
	var b strings.Builder
	if reason != "" {
		b.WriteString(reason + "。")
	}
	b.WriteString("请先安装 git 后再启动 gitwo：")
	switch runtime.GOOS {
	case "windows":
		b.WriteString(" winget install --id Git.Git -e --source winget；也可从 https://git-scm.com/download/win 下载安装。")
	default:
		b.WriteString(" Ubuntu/Debian: sudo apt-get install -y git；CentOS Stream/RHEL: sudo yum install -y git（或 sudo dnf install -y git）；" +
			"其他发行版见 https://git-scm.com/install/linux 。")
	}
	return b.String()
}

// platformInstall 使用当前系统的包管理器安装 git。
func platformInstall() error {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("winget"); err != nil {
			return fmt.Errorf("winget 不可用（需 Windows 10 1809+ 且已安装应用安装程序）")
		}
		return runInstaller("winget", "install", "--id", "Git.Git", "-e",
			"--source", "winget", "--accept-package-agreements", "--accept-source-agreements")
	}

	// Linux / 其他 Unix：按可用包管理器选择；非 root 用 sudo -n（无密码提权）
	prefix := []string{}
	if os.Geteuid() != 0 {
		if _, err := exec.LookPath("sudo"); err != nil {
			return fmt.Errorf("当前用户非 root 且未安装 sudo，无法自动安装")
		}
		prefix = []string{"sudo", "-n"}
	}
	runPM := func(args ...string) error {
		return runInstaller(append(prefix, args...)...)
	}
	if hasCmd("apt-get") {
		// 更新索引失败不致命（离线源/缓存可用时直接装）
		_ = runPM("apt-get", "update", "-y")
		return runPM("apt-get", "install", "-y", "git")
	}
	if hasCmd("dnf") {
		return runPM("dnf", "install", "-y", "git")
	}
	if hasCmd("yum") {
		return runPM("yum", "install", "-y", "git")
	}
	if hasCmd("apk") { // Alpine 等（顺手支持）
		return runPM("apk", "add", "git")
	}
	return fmt.Errorf("未找到受支持的包管理器（apt-get / dnf / yum）")
}

func hasCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// runInstaller 执行安装命令，输出并入错误信息便于排查。
func runInstaller(argv ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("安装命令超时（%s）", installTimeout)
	}
	if err != nil {
		tail := string(out)
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return fmt.Errorf("%s: %v\n%s", strings.Join(argv, " "), err, strings.TrimSpace(tail))
	}
	return nil
}
