// Package sshserver 内嵌 SSH Git 服务端：用户保存公钥后即可
// git clone/push ssh://git@<host>:<port>/<owner>/<repo>.git。
// 认证按公钥指纹识别平台用户（与 GitHub 的 git@ 语义一致）；
// 授权与 HTTP Git 完全一致：读 = CanRead、写 = CanWrite（测试角色全拒）。
package sshserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"

	"gitwo/internal/gitbin"
	"gitwo/internal/gitserver"
	"gitwo/internal/store"
)

// Server 是内嵌 SSH Git 服务端。
type Server struct {
	Addr     string // 监听地址，如 ":2222"
	ReposDir string // 仓库根目录（repos/<owner>/<name>.git）
	Store    *store.Store
	Log      *slog.Logger
	OnPush   func(repo string, updates []gitserver.RefUpdate)
	DataDir  string // 数据目录（receive-pack 经 GITWO_HOME 传给 pre-receive 钩子）

	hostSigner ssh.Signer
	ln         net.Listener // 由 ListenAndServe 创建，供 Close 关闭
}

// Close 关闭监听（已建立的连接不中断）。
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

// EnsureHostKey 载入（或首次生成并保存）ED25519 主机密钥。
func (s *Server) EnsureHostKey(dir string) error {
	keyPath := filepath.Join(dir, "ssh_host_ed25519_key")
	if data, err := os.ReadFile(keyPath); err == nil {
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return fmt.Errorf("parse host key: %w", err)
		}
		s.hostSigner = signer
		return nil
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "gitwo host key")
	if err != nil {
		return err
	}
	pemData := pem.EncodeToMemory(block)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pemData, 0o600); err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(pemData)
	if err != nil {
		return err
	}
	s.hostSigner = signer
	s.Log.Info("ssh host key generated", "path", keyPath)
	return nil
}

// lookupUser 按公钥指纹识别平台用户；测试角色与未知指纹被拒。
func (s *Server) lookupUser(key ssh.PublicKey) (string, error) {
	fp := ssh.FingerprintSHA256(key)
	user, ok := s.Store.SSHKeyUser(fp)
	if !ok {
		return "", fmt.Errorf("unknown public key %s", fp)
	}
	if usr, ok := s.Store.GetUser(user); ok && usr.Tester() {
		return "", fmt.Errorf("tester role has no git access")
	}
	return user, nil
}

// ListenAndServe 阻塞运行 SSH 服务（调用方负责 goroutine 与关闭）。
func (s *Server) ListenAndServe() error {
	if s.hostSigner == nil {
		return errors.New("sshserver: host key not loaded")
	}
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-Gitwo",
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			user, err := s.lookupUser(key)
			if err != nil {
				s.Log.Info("ssh auth denied", "fp", ssh.FingerprintSHA256(key), "err", err)
				return nil, err
			}
			s.Log.Info("ssh auth ok", "user", user, "fp", ssh.FingerprintSHA256(key))
			return &ssh.Permissions{Extensions: map[string]string{"user": user}}, nil
		},
	}
	cfg.AddHostKey(s.hostSigner)

	ln, err := net.Listen("tcp", s.Addr)
	if err != nil {
		return err
	}
	s.ln = ln
	s.Log.Info("ssh git server listening", "addr", s.Addr)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(conn, cfg)
	}
}

func (s *Server) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	user := sconn.Permissions.Extensions["user"]
	go s.discardGlobalRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			newChan.Reject(ssh.UnknownChannelType, "only session channels")
			continue
		}
		go s.handleSession(newChan, user)
	}
}

func (s *Server) discardGlobalRequests(reqs <-chan *ssh.Request) {
	for req := range reqs {
		if req.WantReply {
			req.Reply(false, nil)
		}
	}
}

func (s *Server) handleSession(newChan ssh.NewChannel, user string) {
	ch, inReqs, err := newChan.Accept()
	if err != nil {
		return
	}
	defer ch.Close()
	for req := range inReqs {
		switch req.Type {
		case "env":
			if req.WantReply {
				req.Reply(true, nil)
			}
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
			s.runGitCommand(ch, user, payload.Command)
			return // 一个 session 只处理一次 exec
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// parseGitCommand 解析 `git-upload-pack 'owner/repo.git'` 形式的命令。
func parseGitCommand(cmd string) (verb, repoFull string, err error) {
	fields := strings.Fields(strings.TrimSpace(cmd))
	if len(fields) < 2 {
		return "", "", fmt.Errorf("not a git command")
	}
	verb = fields[0]
	if verb != "git-upload-pack" && verb != "git-receive-pack" && verb != "git-upload-archive" {
		return "", "", fmt.Errorf("command not allowed: %s", verb)
	}
	repo := strings.Trim(fields[1], `"'`)
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.TrimPrefix(repo, "/")
	return verb, repo, nil
}

// 按仓库串行化 receive-pack 的快照对比（跨仓库并行不受影响）。
var repoLocks sync.Map

func (s *Server) runGitCommand(ch ssh.Channel, user, command string) {
	verb, repoFull, perr := parseGitCommand(command)
	if perr != nil {
		fmt.Fprintf(ch.Stderr(), "%s\r\n", perr.Error())
		sshExit(ch, 1)
		return
	}
	owner, name, ok := store.SplitFullName(repoFull)
	if !ok {
		fmt.Fprintf(ch.Stderr(), "invalid repository: %s\r\n", repoFull)
		sshExit(ch, 1)
		return
	}
	rp, exists := s.Store.GetRepo(owner + "/" + name)
	if !exists {
		fmt.Fprintf(ch.Stderr(), "repository not found: %s\r\n", repoFull)
		sshExit(ch, 1)
		return
	}

	write := verb != "git-upload-pack" // receive-pack 是写操作
	if write && !s.Store.CanWrite(user, rp) {
		fmt.Fprintf(ch.Stderr(), "forbidden: no write access to %s\r\n", repoFull)
		sshExit(ch, 1)
		return
	}
	if !write {
		// 读（clone/fetch）：测试角色全拒（与 HTTP 一致）；其余按 CanRead
		if usr, ok := s.Store.GetUser(user); ok && usr.Tester() {
			fmt.Fprintf(ch.Stderr(), "forbidden: tester role has no git access\r\n")
			sshExit(ch, 1)
			return
		}
		if !s.Store.CanRead(user, rp) {
			fmt.Fprintf(ch.Stderr(), "forbidden: no read access to %s\r\n", repoFull)
			sshExit(ch, 1)
			return
		}
	}

	dir := filepath.Join(s.ReposDir, owner, name+".git")
	var argv []string
	switch verb {
	case "git-upload-pack":
		argv = []string{"upload-pack", dir}
	case "git-receive-pack":
		argv = []string{"receive-pack", dir}
	default: // git-upload-archive
		argv = []string{"upload-archive", dir}
	}

	if write {
		lock, _ := repoLocks.LoadOrStore(repoFull, &sync.Mutex{})
		lock.(*sync.Mutex).Lock()
		defer lock.(*sync.Mutex).Unlock()
	}

	before, _ := gitserver.RefSnapshot(dir)
	cmd := exec.Command(gitbin.Path(), argv...)
	cmd.Stdin = ch
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		sshExit(ch, 1)
		return
	}
	cmd.Stderr = ch.Stderr()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(ch.Stderr(), "git start failed: %v\r\n", err)
		sshExit(ch, 1)
		return
	}
	io.Copy(ch, stdout)
	waitErr := cmd.Wait()
	if write {
		if after, err := gitserver.RefSnapshot(dir); err == nil && s.OnPush != nil {
			if ups := gitserver.DiffRefs(before, after); len(ups) > 0 {
				s.OnPush(rp.FullName(), ups)
			}
		}
	}
	code := 0
	if waitErr != nil {
		s.Log.Info("ssh git command failed", "user", user, "cmd", command, "err", waitErr)
		code = 1
	}
	sshExit(ch, code)
}

// sshExit 发送 SSH exit-status 请求。
func sshExit(ch ssh.Channel, code int) {
	payload := ssh.Marshal(struct{ Status uint32 }{uint32(code)})
	_, _ = ch.SendRequest("exit-status", false, payload)
	ch.CloseWrite()
}
