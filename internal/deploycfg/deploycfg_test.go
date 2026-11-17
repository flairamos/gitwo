package deploycfg

import (
	"os"
	"path/filepath"
	"testing"
)

func load(t *testing.T, content string) (*Deploy, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "deploy.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := Load(p)
	if err != nil {
		return nil, err
	}
	verr := d.Validate(Defaults{Workdir: "/apps/r", StaticTarget: "/apps/r/static"}, true)
	return d, verr
}

func TestValidStatic(t *testing.T) {
	d, err := load(t, `
type: static
build:
  frontend:
    command: npm ci && npm run build
    output: dist
`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Type != TypeStatic {
		t.Fatal("wrong type")
	}
	if !d.Build.Frontend.ShouldVerifyIndex() {
		t.Error("verify_index should default to true")
	}
	if d.Build.Frontend.Target != "/apps/r/static" {
		t.Errorf("static target default: %q", d.Build.Frontend.Target)
	}
}

// 原生前端：无 build.frontend 段（零配置，仓库根即站点）与 command 留空
// （直接发布 output 目录）都是合法配置。
func TestPlainStaticNoBuild(t *testing.T) {
	d, err := load(t, "type: static\n")
	if err != nil {
		t.Fatal(err)
	}
	if d.Build.Frontend != nil {
		t.Error("frontend section should stay nil for zero-config static")
	}

	d, err = load(t, `
type: static
build:
  frontend:
    output: public
`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Build.Frontend.Command != "" {
		t.Error("command should stay empty (no-build mode)")
	}
	if d.Build.Frontend.Target != "/apps/r/static" {
		t.Errorf("target default: %q", d.Build.Frontend.Target)
	}
}

func TestValidServiceMinimal(t *testing.T) {
	d, err := load(t, `
type: service
build:
  backend:
    command: go build -o app
    output: app
run:
  command: ./app
`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run.Workdir != "/apps/r" {
		t.Errorf("workdir default: %q", d.Run.Workdir)
	}
}

func TestValidServiceFull(t *testing.T) {
	d, err := load(t, `
type: service
build:
  frontend:
    command: npm run build
    output: dist
  backend:
    command: go build -o app
    output: app
run:
  workdir: /srv/myapp
  command: ./app --port 8080
  port: 8080
  env:
    APP_ENV: production
  keep_binaries: 3
`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run.KeepBinaries != 3 || d.Run.Port != 8080 || d.Run.Workdir != "/srv/myapp" {
		t.Errorf("run fields lost: %+v", d.Run)
	}
}

func TestValidDual(t *testing.T) {
	d, err := load(t, `
type: dual
build:
  frontend:
    command: npm ci && npm run build
    output: dist
  backend:
    command: go build -o bin/api
    output: bin/api
static_server:
  addr: 0.0.0.0:3000
  spa: false
run:
  command: ./api
`)
	if err != nil {
		t.Fatal(err)
	}
	if d.Build.Frontend.Target != "/apps/r/static" {
		t.Errorf("dual target default: %q", d.Build.Frontend.Target)
	}
	if d.StaticServer.SPEnabled() {
		t.Error("spa=false not respected")
	}
}

func TestInvalid(t *testing.T) {
	cases := []struct {
		name, yaml string
	}{
		{"unknown type", "type: kubernetes\n"},
		{"static with backend", "type: static\nbuild:\n  frontend:\n    command: x\n    output: d\n  backend:\n    command: y\n    output: b\n"},
		{"frontend without output", "type: static\nbuild:\n  frontend:\n    command: x\n"},
		{"service without run", "type: service\nbuild:\n  backend:\n    command: go build\n    output: app\n"},
		{"service without command", "type: service\nbuild:\n  backend:\n    command: go build\n    output: app\nrun: {}\n"},
		{"dual without frontend", "type: dual\nbuild:\n  backend:\n    command: y\n    output: b\nrun:\n  command: ./a\n"},
		{"bad port", "type: service\nbuild:\n  backend:\n    command: x\n    output: b\nrun:\n  command: ./a\n  port: 99999\n"},
		{"unknown key", "type: static\ntypo: oops\n"},
	}
	for _, tc := range cases {
		if _, err := load(t, tc.yaml); err == nil {
			t.Errorf("%s: expected error, got none", tc.name)
		}
	}
}
