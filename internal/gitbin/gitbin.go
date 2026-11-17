// Package gitbin 解析系统 git 可执行文件的路径。
//
// Gitwo 依赖系统 git（仓库浏览、push 检测、构建同步都需要），但服务进程
// 的 PATH 未必包含 git（桌面快捷方式 / 计划任务 / 精简环境启动，或 git
// 安装在非标准目录时）。解析优先级：
//
//	server.yaml 的 git_path > 环境变量 GITWO_GIT > PATH > 各平台常见安装位置
//
// 结果进程内缓存，所有内部 git 调用统一使用 Path()。
package gitbin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

var (
	mu       sync.Mutex
	path     string
	err      error
	resolved bool
)

// Resolve 解析并缓存 git 路径；找不到时返回带安装/指定指引的错误。
func Resolve(explicit string) error {
	mu.Lock()
	defer mu.Unlock()
	if resolved {
		return err
	}
	if p := firstNonEmpty(explicit, os.Getenv("GITWO_GIT")); p != "" {
		if st, e := os.Stat(p); e != nil || st.IsDir() {
			err = fmt.Errorf("git_path 指向的路径不可用: %s（请检查 server.yaml 的 git_path 或 GITWO_GIT）", p)
		} else {
			path = p
		}
		resolved = true
		return err
	}
	path, err = lookup(exec.LookPath, candidates())
	resolved = true
	return err
}

// Path 返回解析后的 git 路径；尚未解析或解析失败时回退为 "git"
// （由 exec 按 PATH 再试一次，保持旧行为）。
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	if path != "" {
		return path
	}
	return "git"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// lookup 先查 PATH，再按候选路径探测；lookPath 可注入以便测试。
func lookup(lookPath func(string) (string, error), cands []string) (string, error) {
	if p, e := lookPath("git"); e == nil {
		return p, nil
	}
	for _, c := range cands {
		if st, e := os.Stat(c); e == nil && !st.IsDir() {
			return c, nil
		}
	}
	return "", fmt.Errorf("未找到 git 可执行文件：Gitwo 依赖系统 git（克隆/浏览/构建同步都需要）。" +
		"请任选其一后重启 gitwo：" +
		"1) 安装 Git 并确保 git 在 PATH 中；" +
		"2) 在 data/server.yaml 设置 git_path: <git 绝对路径>；" +
		"3) 设置环境变量 GITWO_GIT=<git 绝对路径>")
}

// candidates 返回各平台的常见 git 安装位置。
func candidates() []string {
	if runtime.GOOS == "windows" {
		var out []string
		addBase := func(base string) {
			if base == "" {
				return
			}
			out = append(out,
				filepath.Join(base, "Git", "cmd", "git.exe"),
				filepath.Join(base, "Git", "bin", "git.exe"))
		}
		addBase(os.Getenv("ProgramFiles"))
		addBase(os.Getenv("ProgramFiles(x86)"))
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			out = append(out, filepath.Join(la, "Programs", "Git", "cmd", "git.exe"))
		}
		if up := os.Getenv("USERPROFILE"); up != "" {
			out = append(out, filepath.Join(up, "scoop", "apps", "git", "current", "cmd", "git.exe"))
		}
		out = append(out, `C:\ProgramData\chocolatey\bin\git.exe`)
		return out
	}
	return []string{"/usr/bin/git", "/usr/local/bin/git", "/bin/git", "/snap/bin/git"}
}
