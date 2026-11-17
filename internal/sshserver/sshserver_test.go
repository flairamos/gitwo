package sshserver

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"gitwo/internal/gitserver"
	"gitwo/internal/store"
)

func setupSSH(t *testing.T) (*Server, *store.Store, func()) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh not available")
	}
	dir := t.TempDir()
	reposDir := filepath.Join(dir, "repos")
	if err := os.MkdirAll(filepath.Join(reposDir, "admin"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Load(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRepo("demo", "user:admin", false, ""); err != nil {
		t.Fatal(err)
	}
	repoPath := filepath.Join(reposDir, "admin", "demo.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", repoPath).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	_ = exec.Command("git", "--git-dir", repoPath, "symbolic-ref", "HEAD", "refs/heads/main").Run()

	// 空闲端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	s := &Server{
		Addr:     fmt.Sprintf("127.0.0.1:%d", port),
		ReposDir: reposDir,
		Store:    st,
		Log:      slog.Default(),
	}
	if err := s.EnsureHostKey(filepath.Join(dir, "data", "ssh")); err != nil {
		t.Fatal(err)
	}
	go func() {
		_ = s.ListenAndServe()
	}()

	return s, st, func() { _ = s.Close() }
}

// TestEnsureHostKey：重复调用加载同一把主机密钥（不重新生成）。
func TestEnsureHostKey(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Log: slog.Default()}
	if err := s.EnsureHostKey(filepath.Join(dir, "ssh")); err != nil {
		t.Fatal(err)
	}
	first := s.hostSigner.PublicKey().Marshal()
	if err := s.EnsureHostKey(filepath.Join(dir, "ssh")); err != nil {
		t.Fatal(err)
	}
	if string(first) != string(s.hostSigner.PublicKey().Marshal()) {
		t.Error("host key regenerated on second call")
	}
}

// addKey 给用户注册一把公钥（authorized_keys 内容 → 指纹入库）。
func addKey(t *testing.T, st *store.Store, user, pubPath string) {
	t.Helper()
	pub, err := os.ReadFile(pubPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	parsed, comment, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	err = st.CreateSSHKey(&store.SSHKey{
		User:        user,
		Name:        comment,
		Fingerprint: ssh.FingerprintSHA256(parsed),
		Type:        parsed.Type(),
		Key:         strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed))),
	})
	if err != nil {
		t.Fatalf("add key: %v", err)
	}
}

func sshEnv(t *testing.T, port int, keyDir string) []string {
	t.Helper()
	key := filepath.ToSlash(filepath.Join(keyDir, "id_ed25519"))
	return append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+key+
			" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -p "+fmt.Sprint(port)+
			" -o IdentitiesOnly=yes",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func runGitSSH(t *testing.T, dir string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(env,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestSSHGitRoundTrip：真实 git over SSH —— clone 空仓库 → 提交 → push（触发
// OnPush 部署管线）→ 重新 clone 校验内容。
func TestSSHGitRoundTrip(t *testing.T) {
	s, st, closeFn := setupSSH(t)
	defer closeFn()

	var pushed []string
	s.OnPush = func(repo string, _ []gitserver.RefUpdate) {
		pushed = append(pushed, repo)
	}

	keyDir := t.TempDir()
	kgen := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "admin@test", "-f", filepath.Join(keyDir, "id_ed25519"))
	if out, err := kgen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	addKey(t, st, "admin", filepath.Join(keyDir, "id_ed25519"))

	port := strings.TrimPrefix(s.Addr, "127.0.0.1:")
	env := sshEnv(t, mustInt(port), keyDir)
	url := "ssh://git@127.0.0.1:" + port + "/admin/demo.git"

	work := t.TempDir()
	wt := filepath.Join(work, "wt")
	if out, err := runGitSSH(t, work, env, "clone", url, "wt"); err != nil {
		t.Fatalf("ssh clone: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(wt, "hello.txt"), []byte("over ssh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runGitSSH(t, wt, env, "add", "-A"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := runGitSSH(t, wt, env, "commit", "-m", "ssh push"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	out, err := runGitSSH(t, wt, env, "push", "origin", "HEAD:main")
	if err != nil {
		t.Fatalf("ssh push: %v\n%s", err, out)
	}
	if len(pushed) != 1 || pushed[0] != "admin/demo" {
		t.Errorf("OnPush = %v, want [admin/demo]", pushed)
	}

	wt2 := filepath.Join(work, "wt2")
	if out, err := runGitSSH(t, work, env, "clone", url, "wt2"); err != nil {
		t.Fatalf("ssh clone 2: %v\n%s", err, out)
	}
	data, rerr := os.ReadFile(filepath.Join(wt2, "hello.txt"))
	if rerr != nil || string(data) != "over ssh" {
		t.Errorf("re-clone content = %q err=%v", data, rerr)
	}

	// 未知公钥被拒
	badDir := t.TempDir()
	bgen := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", filepath.Join(badDir, "id_ed25519"))
	if out, err := bgen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen bad: %v\n%s", err, out)
	}
	uout, uerr := runGitSSH(t, work, sshEnv(t, mustInt(port), badDir), "ls-remote", url)
	if uerr == nil {
		t.Errorf("unknown key should fail\n%s", uout)
	}

	// 测试角色即使有自己的公钥也被拒（读写全拒）
	if err := st.CreateUser("qa", "pw-123456", false, store.RoleTester); err != nil {
		t.Fatal(err)
	}
	qaDir := t.TempDir()
	qgen := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", filepath.Join(qaDir, "id_ed25519"))
	if out, err := qgen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen qa: %v\n%s", err, out)
	}
	addKey(t, st, "qa", filepath.Join(qaDir, "id_ed25519"))
	qout, qerr := runGitSSH(t, work, sshEnv(t, mustInt(port), qaDir), "ls-remote", url)
	if qerr == nil {
		t.Errorf("tester git should fail\n%s", qout)
	}
	// 同一指纹不能绑定第二个账户
	dupErr := st.CreateSSHKey(&store.SSHKey{
		User: "qa", Name: "dup",
		Fingerprint: s.fingerprintOf(t, filepath.Join(keyDir, "id_ed25519.pub")),
		Type:        "ssh-ed25519", Key: "dup",
	})
	if dupErr == nil {
		t.Error("duplicate fingerprint accepted")
	}
}

func mustInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			panic(errors.New("bad port: " + s))
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (s *Server) fingerprintOf(t *testing.T, pubPath string) string {
	t.Helper()
	pub, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return ssh.FingerprintSHA256(parsed)
}

// TestParseGitCommand：exec 命令解析与白名单。
func TestParseGitCommand(t *testing.T) {
	for _, ok := range []string{
		`git-upload-pack 'team/shop.git'`,
		`git-receive-pack 'team/shop.git'`,
		`git-upload-pack '/admin/demo.git'`,
		`git-upload-pack "admin/demo"`,
	} {
		if _, _, err := parseGitCommand(ok); err != nil {
			t.Errorf("parseGitCommand(%q) err: %v", ok, err)
		}
	}
	for _, bad := range []string{
		`rm -rf /`,
		`git-upload-archive`,
		`nano 'team/shop.git'`,
	} {
		if _, _, err := parseGitCommand(bad); err == nil {
			t.Errorf("parseGitCommand(%q) should fail", bad)
		}
	}
	if _, repo, err := parseGitCommand(`git-upload-pack 'team/shop.git'`); err != nil || repo != "team/shop" {
		t.Errorf("repo = %q err %v", repo, err)
	}
}
