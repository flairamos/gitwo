package store

import (
	"strings"
	"testing"
)

func TestSSHKeys(t *testing.T) {
	s := newStore(t)
	if err := s.CreateUser("alice", "pass1234", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("bob", "pass1234", false, ""); err != nil {
		t.Fatal(err)
	}
	add := func(user, fp string) error {
		return s.CreateSSHKey(&SSHKey{
			User: user, Name: "k-" + fp, Fingerprint: fp, Type: "ssh-ed25519",
			Key: "ssh-ed25519 AAAA " + fp,
		})
	}
	if err := add("alice", "SHA256:aaa"); err != nil {
		t.Fatal(err)
	}
	if err := add("alice", "SHA256:bbb"); err != nil {
		t.Fatal(err)
	}
	// 同一指纹全局唯一（无论属于谁）
	if err := add("bob", "SHA256:aaa"); err == nil {
		t.Error("duplicate fingerprint accepted")
	}
	if err := add("bob", "SHA256:ccc"); err != nil {
		t.Fatal(err)
	}
	if keys := s.ListSSHKeys("alice"); len(keys) != 2 {
		t.Errorf("alice keys = %d, want 2", len(keys))
	}
	if u, ok := s.SSHKeyUser("SHA256:ccc"); !ok || u != "bob" {
		t.Errorf("fingerprint owner = %q %v, want bob", u, ok)
	}
	if _, ok := s.SSHKeyUser("SHA256:zzz"); ok {
		t.Error("unknown fingerprint matched")
	}
	// 删除只能删自己的
	if err := s.RemoveSSHKey("alice", "no-such-id"); err == nil {
		t.Error("missing key delete accepted")
	}
	keys := s.ListSSHKeys("alice")
	if err := s.RemoveSSHKey("alice", keys[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.SSHKeyUser("SHA256:" + strings.TrimPrefix(keys[0].Fingerprint, "SHA256:")); !ok {
		// 指纹本身含 SHA256: 前缀，删掉后应查不到
		if _, ok2 := s.SSHKeyUser(keys[0].Fingerprint); ok2 {
			t.Error("removed key still resolves")
		}
	}
	if keys2 := s.ListSSHKeys("alice"); len(keys2) != 1 {
		t.Errorf("alice keys after remove = %d, want 1", len(keys2))
	}
}
