// Package deploycfg parses and validates the deploy.yaml file found in each
// deployed repository. It declares how the project builds and how gitwo
// should ship the artifacts (static / service / dual).
package deploycfg

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const (
	TypeStatic  = "static"  // frontend only: build -> publish to static target
	TypeService = "service" // one Go service; optional frontend served alongside
	TypeDual    = "dual"    // separate static frontend + backend service
)

// Frontend build spec.
type Frontend struct {
	// Command 为空 = 原生静态目录：跳过构建，直接发布 output 目录。
	// Vite/Vue 等需要构建的项目填写构建命令（如 "npm ci && npm run build"）。
	Command     string `yaml:"command"`
	Dir         string `yaml:"dir"`          // optional subdir of the repo containing package.json etc.
	Output      string `yaml:"output"`       // built output dir, relative to repo root (or absolute)
	Target      string `yaml:"target"`       // publish target; defaults to apps/<repo>/static
	VerifyIndex *bool  `yaml:"verify_index"` // fail deploy when index.html missing in target

	verify bool
}

// ShouldVerifyIndex reports whether gitwo must check index.html after publish.
func (f *Frontend) ShouldVerifyIndex() bool { return f.verify }

// Backend build spec.
type Backend struct {
	Command string `yaml:"command"` // shell command run in the repo (build.dir)
	Dir     string `yaml:"dir"`     // optional subdir containing go.mod
	Output  string `yaml:"output"`  // built binary, relative to repo root (or absolute)
}

// Run describes how the backend service is managed (nohup + pid file).
type Run struct {
	Workdir      string            `yaml:"workdir"`       // defaults to apps/<repo>
	Command      string            `yaml:"command"`       // start command, e.g. ./app / java -jar app.jar / python3 app.py
	Port         int               `yaml:"port"`          // explicit listen port; 0 = auto-assign (injected as $PORT)
	Env          map[string]string `yaml:"env"`           // service environment variables
	KeepBinaries int               `yaml:"keep_binaries"` // keep N old binaries in <workdir>/backups (0 = off)
	// Files 可选：从仓库根同步到运行目录的文件/目录（解释型语言常用，
	// 如 python 的 app.py、node 的 server.js 与 package.json）。
	Files []string `yaml:"files"`
}

// StaticServer configures an optional dedicated static server (dual mode).
// In the integrated platform the gitwo serves the frontend itself under
// /sites/<repo>/, so this section only records the SPA switch and an
// optional static dir override.
type StaticServer struct {
	Addr      string `yaml:"addr"`       // informational (standalone static server)
	StaticDir string `yaml:"static_dir"` // optional frontend dir override
	SPAServe  *bool  `yaml:"spa"`        // fallback unknown paths to index.html (default true)

	spa bool
}

// SPEnabled reports whether SPA fallback is on.
func (s *StaticServer) SPEnabled() bool { return s.spa }

type Build struct {
	Frontend *Frontend `yaml:"frontend"`
	Backend  *Backend  `yaml:"backend"`
}

type Deploy struct {
	Type         string            `yaml:"type"` // static | service | dual
	Env          map[string]string `yaml:"env"`  // environment injected into builds and the service
	Build        Build             `yaml:"build"`
	Run          *Run              `yaml:"run"`
	StaticServer *StaticServer     `yaml:"static_server"`
}

// Load parses a deploy.yaml. Unknown keys are rejected so typos fail loudly.
func Load(path string) (*Deploy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var d Deploy
	if err := dec.Decode(&d); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: file is empty", path)
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &d, nil
}

// LoadWorkspace looks for deploy.yaml (then deploy.yml) in the repo root.
func LoadWorkspace(workdir string) (*Deploy, error) {
	for _, name := range []string{"deploy.yaml", "deploy.yml"} {
		p := filepath.Join(workdir, name)
		d, err := Load(p)
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no deploy.yaml (or deploy.yml) found in %s", workdir)
}

// Defaults fills in paths that depend on where gitwo runs: the service
// workdir (apps/<repo>) and the static publish target (apps/<repo>/static).
type Defaults struct {
	Workdir      string
	StaticTarget string
}

// Validate checks the spec for the declared type, normalizes defaults and
// rejects sections that do not apply to the type.
func (d *Deploy) Validate(def Defaults, defaultVerifyIndex bool) error {
	switch d.Type {
	case TypeStatic:
		// build.frontend is optional: a repo without it is a plain static
		// site published straight from the repo root (the pipeline verifies
		// index.html exists). With a frontend section, command may be empty
		// to publish the output directory without building.
		if d.Build.Backend != nil {
			return fmt.Errorf("build.backend is not used in %s mode", d.Type)
		}
		if d.Run != nil {
			return fmt.Errorf("run section is not used in %s mode", d.Type)
		}
		if d.StaticServer != nil {
			return fmt.Errorf("static_server is only used in %s mode", TypeDual)
		}
		if d.Build.Frontend != nil {
			if err := checkFrontend(d.Build.Frontend); err != nil {
				return err
			}
			if d.Build.Frontend.Target == "" {
				d.Build.Frontend.Target = def.StaticTarget
			}
		}

	case TypeService:
		if d.Build.Backend == nil {
			return fmt.Errorf("type %s requires build.backend", d.Type)
		}
		if d.Run == nil {
			return fmt.Errorf("type %s requires run section", d.Type)
		}
		if d.StaticServer != nil {
			return fmt.Errorf("static_server is only used in %s mode", TypeDual)
		}
		if err := checkBackend(d.Build.Backend); err != nil {
			return err
		}
		if err := checkRun(d.Run); err != nil {
			return err
		}
		if d.Build.Frontend != nil {
			if err := checkFrontend(d.Build.Frontend); err != nil {
				return err
			}
			if d.Build.Frontend.Target == "" {
				d.Build.Frontend.Target = def.StaticTarget
			}
		}

	case TypeDual:
		if d.Build.Frontend == nil {
			return fmt.Errorf("type %s requires build.frontend", d.Type)
		}
		if d.Build.Backend == nil {
			return fmt.Errorf("type %s requires build.backend", d.Type)
		}
		if d.Run == nil {
			return fmt.Errorf("type %s requires run section", d.Type)
		}
		if err := checkFrontend(d.Build.Frontend); err != nil {
			return err
		}
		if err := checkBackend(d.Build.Backend); err != nil {
			return err
		}
		if err := checkRun(d.Run); err != nil {
			return err
		}
		// static_server is optional: the frontend is published to
		// def.StaticTarget and served by gitwo itself under /sites/<repo>/.
		if d.Build.Frontend.Target == "" {
			if d.StaticServer != nil && d.StaticServer.StaticDir != "" {
				d.Build.Frontend.Target = d.StaticServer.StaticDir
			} else {
				d.Build.Frontend.Target = def.StaticTarget
			}
		}

	default:
		return fmt.Errorf("type must be one of %s | %s | %s, got %q", TypeStatic, TypeService, TypeDual, d.Type)
	}

	// normalize pointers into resolved values
	if d.Build.Frontend != nil {
		v := defaultVerifyIndex
		if d.Build.Frontend.VerifyIndex != nil {
			v = *d.Build.Frontend.VerifyIndex
		}
		d.Build.Frontend.verify = v
	}
	if d.StaticServer != nil {
		d.StaticServer.spa = d.StaticServer.SPAServe == nil || *d.StaticServer.SPAServe
	}
	if d.Run != nil {
		if d.Run.Port < 0 || d.Run.Port > 65535 {
			return fmt.Errorf("run.port must be 0-65535, got %d", d.Run.Port)
		}
		if d.Run.Workdir == "" {
			d.Run.Workdir = def.Workdir
		}
	}
	return nil
}

func checkFrontend(f *Frontend) error {
	// command 为空 = 原生静态目录：跳过构建，直接发布 output 目录
	if f.Output == "" {
		return fmt.Errorf("build.frontend.output is required")
	}
	return nil
}

func checkBackend(b *Backend) error {
	if b.Command == "" {
		return fmt.Errorf("build.backend.command is required")
	}
	if b.Output == "" {
		return fmt.Errorf("build.backend.output is required")
	}
	return nil
}

func checkRun(r *Run) error {
	if r.Command == "" {
		return fmt.Errorf("run.command is required")
	}
	return nil
}
