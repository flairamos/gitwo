package procmgr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpectBinary(t *testing.T) {
	cases := map[string]string{
		"./app --port 8080":        "app",
		"./bin/app":                "app",
		"/usr/local/bin/my-server": "my-server",
		"  ./app   -flag value":    "app",
		"":                         "",
	}
	for cmd, want := range cases {
		if got := ExpectBinary(cmd); got != want {
			t.Errorf("ExpectBinary(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestQuoteSh(t *testing.T) {
	if got := QuoteSh("a'b"); got != `'a'"'"'b'` {
		t.Errorf("QuoteSh single quote: %q", got)
	}
	if got := QuoteSh("plain"); got != `'plain'` {
		t.Errorf("QuoteSh plain: %q", got)
	}
}

func TestReadPIDFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "app.pid")

	if err := os.WriteFile(p, []byte("12345\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pid, err := ReadPIDFile(p)
	if err != nil || pid != 12345 {
		t.Fatalf("pid=%d err=%v", pid, err)
	}

	if err := os.WriteFile(p, []byte("notapid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPIDFile(p); err == nil {
		t.Error("garbage pid file accepted")
	}

	if _, err := ReadPIDFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing pid file accepted")
	}

	if err := os.WriteFile(p, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPIDFile(p); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("zero pid should be invalid, err=%v", err)
	}
}

func TestNameMatches(t *testing.T) {
	cases := []struct {
		name, expect string
		want         bool
	}{
		{"java", "java", true},
		{"python3.14", "python3", true}, // exe 版本后缀
		{"python3", "python3", true},
		{"npm", "npm", true},
		{"node", "node", true},
		{"javaw", "java", false}, // 非版本后缀不匹配
		{"javascript", "java", false},
		{"nginx", "java", false},
		{"verylongservic", "verylongservicename-x", true}, // comm 15 字符截断
		{"py", "python3", false},
	}
	for _, tc := range cases {
		if got := nameMatches(tc.name, tc.expect); got != tc.want {
			t.Errorf("nameMatches(%q, %q) = %v, want %v", tc.name, tc.expect, got, tc.want)
		}
	}
}
