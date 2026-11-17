package store

import (
	"fmt"
	"sort"
	"time"

	"gitwo/internal/model"
)

// SSHKey 是用户保存的一条 SSH 公钥（authorized_keys 单行格式）。
// SSH Git 服务端按指纹识别平台用户；多个用户各自添加公钥即可
// 推送同一个仓库（权限仍由组织的成员关系决定）。
type SSHKey struct {
	ID          string    `yaml:"id"`
	User        string    `yaml:"user"`
	Name        string    `yaml:"name"`
	Fingerprint string    `yaml:"fingerprint"` // SHA256:...（GitHub 风格）
	Type        string    `yaml:"type"`        // ssh-ed25519 / ssh-rsa / ecdsa-sha2-...
	Key         string    `yaml:"key"`         // 规范化后的 authorized_keys 单行
	AddedAt     time.Time `yaml:"added_at"`
}

const maxSSHKeysPerUser = 20

// CreateSSHKey 落库一条公钥（ID 由调用方生成，为空则这里不生成——
// 调用方 webui 用 key 指纹校验唯一性后传入随机 ID）。
func (s *Store) CreateSSHKey(k *SSHKey) error {
	if k.User == "" || k.Fingerprint == "" || k.Key == "" {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 同一指纹只允许添加一次（无论属于哪个用户——一把钥匙对应一个人）
	for i := range s.SSHKeys {
		if s.SSHKeys[i].Fingerprint == k.Fingerprint {
			return fmt.Errorf("该公钥 %w", ErrExists)
		}
	}
	n := 0
	for i := range s.SSHKeys {
		if s.SSHKeys[i].User == k.User {
			n++
		}
	}
	if n >= maxSSHKeysPerUser {
		return fmt.Errorf("每个用户最多保存 %d 把公钥", maxSSHKeysPerUser)
	}
	if k.ID == "" {
		k.ID = model.NewID()
	}
	k.AddedAt = time.Now()
	s.SSHKeys = append(s.SSHKeys, *k)
	sort.Slice(s.SSHKeys, func(i, j int) bool {
		if s.SSHKeys[i].User != s.SSHKeys[j].User {
			return s.SSHKeys[i].User < s.SSHKeys[j].User
		}
		return s.SSHKeys[i].AddedAt.Before(s.SSHKeys[j].AddedAt)
	})
	return s.saveLocked("sshkeys.yaml")
}

// RemoveSSHKey 删除当前用户的一把公钥；name 为空时按 ID 删。
func (s *Store) RemoveSSHKey(user, keyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	out := s.SSHKeys[:0]
	for _, k := range s.SSHKeys {
		if k.ID == keyID && k.User == user {
			found = true
			continue
		}
		out = append(out, k)
	}
	if !found {
		return ErrNotFound
	}
	s.SSHKeys = out
	return s.saveLocked("sshkeys.yaml")
}

// ListSSHKeys 列出某用户的全部公钥（按添加时间升序）。
func (s *Store) ListSSHKeys(user string) []SSHKey {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SSHKey
	for _, k := range s.SSHKeys {
		if k.User == user {
			out = append(out, k)
		}
	}
	return out
}

// SSHKeyUser 按指纹查公钥属主（SSH 认证用）。
func (s *Store) SSHKeyUser(fingerprint string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.SSHKeys {
		if k.Fingerprint == fingerprint {
			return k.User, true
		}
	}
	return "", false
}

// ---- 测试与生成辅助 ----

// NewSSHKeyID 生成公钥记录 ID（导出给 webui 测试使用）。
func NewSSHKeyID() string { return model.NewID() }

// TestSSHKeys —— 见 sshkey_test.go
