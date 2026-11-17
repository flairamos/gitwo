// Package appconfig resolves the gitwo home directory (where repos,
// workspaces, deployed apps and data live) and loads the optional
// data/server.yaml. Everything not configured has a working default so a
// fresh `gitwo serve` boots with zero configuration.
//
// Layout (relative to home = program directory unless -home overrides):
//
//	<home>/repos/<name>.git        bare git repositories (the git server)
//	<home>/workspaces/<name>/      build checkouts
//	<home>/apps/<name>/            deployed services & static targets
//	<home>/data/                   users.yaml / groups.yaml / repos.yaml / server.yaml
//	/tmp/gitwo/logs/            task logs (temporary, per requirement)
//	/tmp/gitwo/tmp/<task>/      build scratch (TMPDIR injected into builds)
package appconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration decodes YAML strings like "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type Timeouts struct {
	Git       Duration `yaml:"git"`
	Build     Duration `yaml:"build"`
	Deploy    Duration `yaml:"deploy"`
	Stop      Duration `yaml:"stop"`
	StartWait Duration `yaml:"start_wait"`
}

type Config struct {
	Listen        string   `yaml:"listen"`     // default :9527
	GitPath       string   `yaml:"git_path"`   // 可选：显式指定 git 可执行文件路径（自定义安装位置）
	SSHListen     *string  `yaml:"ssh_listen"` // nil = 默认 ":2222"；显式 "" 禁用 SSH Git 服务
	Workers       int      `yaml:"workers"`
	MaxQueued     int      `yaml:"max_queued_per_repo"`
	VerifyIndex   *bool    `yaml:"verify_index"`
	PortMin       int      `yaml:"port_min"` // auto-assign range for deployed services
	PortMax       int      `yaml:"port_max"`
	NotifyWebhook string   `yaml:"notify_webhook"` // 部署结束 POST 的 webhook URL（空禁用）
	Timeouts      Timeouts `yaml:"timeouts"`

	verifyIndex bool
}

func (c *Config) DefaultVerifyIndex() bool { return c.verifyIndex }

// SSHAddr 返回 SSH Git 服务监听地址；空串表示已禁用。
func (c *Config) SSHAddr() string {
	if c.SSHListen == nil {
		return ":2222"
	}
	return *c.SSHListen
}

// SSHSuffix 返回 SSH 监听的 ":端口" 部分（拼接 ssh:// 克隆地址用）。
func (c *Config) SSHSuffix() string {
	addr := c.SSHAddr()
	if addr == "" {
		return ""
	}
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			if i > 0 && addr[i-1] == ']' {
				return addr[i:]
			}
			return addr[i:]
		}
	}
	return addr
}

// ListenSuffix returns the ":port" part of Listen, for building URLs.
func (c *Config) ListenSuffix() string {
	for i := len(c.Listen) - 1; i >= 0; i-- {
		if c.Listen[i] == ':' {
			if i > 0 && c.Listen[i-1] == ']' {
				return c.Listen[i:] // [::1]:9527
			}
			return c.Listen[i:]
		}
	}
	return c.Listen
}

// Paths are derived from the home directory.
type Home struct {
	Dir string
}

func (h Home) DataDir() string       { return filepath.Join(h.Dir, "data") }
func (h Home) ReposDir() string      { return filepath.Join(h.Dir, "repos") }
func (h Home) WorkspaceRoot() string { return filepath.Join(h.Dir, "workspaces") }
func (h Home) AppsRoot() string      { return filepath.Join(h.Dir, "apps") }
func (h Home) ServerYAML() string    { return filepath.Join(h.DataDir(), "server.yaml") }

// TempRoot returns the root for temporary files under the OS temp dir.
func TempRoot() string { return filepath.Join(os.TempDir(), "gitwo") }
func LogRoot() string  { return filepath.Join(TempRoot(), "logs") }
func TmpRoot() string  { return filepath.Join(TempRoot(), "tmp") }

// Resolve determines the home dir: -home flag > GITWO_HOME env > exe dir.
func Resolve(flagHome string) Home {
	dir := flagHome
	if dir == "" {
		dir = os.Getenv("GITWO_HOME")
	}
	if dir == "" {
		dir = os.Getenv("DEPLOYER_HOME") // 改名前的旧环境变量，兼容一个过渡期
	}
	if dir == "" {
		exe, err := os.Executable()
		if err == nil {
			dir = filepath.Dir(exe)
		} else {
			dir, _ = os.Getwd()
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return Home{Dir: dir}
}

// LoadConfig reads data/server.yaml when present, applying defaults.
func (h Home) LoadConfig() (*Config, error) {
	c := &Config{}
	setDefaults(c)

	data, err := os.ReadFile(h.ServerYAML())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("%s: %w", h.ServerYAML(), err)
	}
	setDefaults(c)
	return c, nil
}

func setDefaults(c *Config) {
	if c.Listen == "" {
		c.Listen = ":9527"
	}
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.Workers > 32 {
		c.Workers = 32
	}
	if c.MaxQueued <= 0 {
		c.MaxQueued = 5
	}
	c.verifyIndex = c.VerifyIndex == nil || *c.VerifyIndex
	if c.PortMin <= 0 {
		c.PortMin = 20000
	}
	if c.PortMax <= c.PortMin {
		c.PortMax = c.PortMin + 3999
	}
	if c.Timeouts.Git == 0 {
		c.Timeouts.Git = Duration(5 * time.Minute)
	}
	if c.Timeouts.Build == 0 {
		c.Timeouts.Build = Duration(10 * time.Minute)
	}
	if c.Timeouts.Deploy == 0 {
		c.Timeouts.Deploy = Duration(5 * time.Minute)
	}
	if c.Timeouts.Stop == 0 {
		c.Timeouts.Stop = Duration(15 * time.Second)
	}
	if c.Timeouts.StartWait == 0 {
		c.Timeouts.StartWait = Duration(2 * time.Second)
	}
}

// EnsureDirs creates the full layout.
func (h Home) EnsureDirs() error {
	for _, d := range []string{h.DataDir(), h.ReposDir(), h.WorkspaceRoot(), h.AppsRoot(), LogRoot(), TmpRoot()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}
