package webui

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/crypto/ssh"

	"gitwo/internal/store"
)

// ---- SSH 公钥管理（账户设置） ----

func (u *UI) meKeysPage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	u.render(w, http.StatusOK, "me_keys", meKeysData{
		Base:  u.base(r, "SSH 公钥"),
		Keys:  u.Store.ListSSHKeys(usr.Name),
		Error: r.URL.Query().Get("err"),
	})
}

// meKeysAddPost 解析并保存一条公钥。公钥为 authorized_keys 单行格式
// （ssh-ed25519 AAAA... 备注），名称缺省取公钥的 comment。
func (u *UI) meKeysAddPost(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	if !parseAnyForm(w, r, 1<<20) {
		return
	}
	pub := strings.TrimSpace(r.PostFormValue("pub"))
	name := strings.TrimSpace(r.PostFormValue("name"))
	fail := func(msg string) {
		u.flashRedirect(w, r, "/me/keys?err="+url.QueryEscape(msg), "")
	}
	if pub == "" {
		fail("公钥内容不能为空")
		return
	}
	if len(pub) > 8*1024 {
		fail("公钥内容过长")
		return
	}
	parsed, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(pub))
	if err != nil {
		fail("无法解析公钥：请粘贴完整的 authorized_keys 单行（如 ssh-ed25519 AAAA... 备注）")
		return
	}
	if name == "" {
		name = comment
	}
	if name == "" {
		name = parsed.Type()
	}
	k := store.SSHKey{
		User:        usr.Name,
		Name:        name,
		Fingerprint: ssh.FingerprintSHA256(parsed),
		Type:        parsed.Type(),
		Key:         strings.TrimSpace(string(ssh.MarshalAuthorizedKey(parsed))),
	}
	if err := u.Store.CreateSSHKey(&k); err != nil {
		fail(err.Error())
		return
	}
	u.Log.Info("ssh key added", "user", usr.Name, "fp", k.Fingerprint, "type", k.Type)
	u.flashRedirect(w, r, "/me/keys", "公钥已添加（指纹 "+k.Fingerprint+"）")
}

func (u *UI) meKeysDeletePost(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireLogin(w, r)
	if !ok {
		return
	}
	if err := u.Store.RemoveSSHKey(usr.Name, r.PathValue("id")); err != nil {
		u.flashRedirect(w, r, "/me/keys", "删除失败: "+err.Error())
		return
	}
	u.Log.Info("ssh key removed", "user", usr.Name, "id", r.PathValue("id"))
	u.flashRedirect(w, r, "/me/keys", "公钥已删除")
}

// sshCloneURL 组装 ssh://git@<host>:<ssh端口>/<owner>/<repo>.git；未启用 SSH 返回空。
func (u *UI) sshCloneURL(cfgListenSuffix, hostPort, full string) string {
	suffix := cfgListenSuffix
	if suffix == "" {
		return ""
	}
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}
	return "ssh://git@" + host + suffix + "/" + full + ".git"
}
