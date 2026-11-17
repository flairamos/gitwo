package store

import (
	"os"
	"path/filepath"
	"testing"
)

func osWriteFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBootstrapAdmin(t *testing.T) {
	s := newStore(t)
	u, ok := s.VerifyLogin("admin", "admin")
	if !ok || !u.Admin {
		t.Fatal("default admin/admin not bootstrapped")
	}
	if _, ok := s.VerifyLogin("admin", "wrong"); ok {
		t.Error("wrong password accepted")
	}
}

func TestUsersAndOrgs(t *testing.T) {
	s := newStore(t)
	if err := s.CreateUser("alice", "pass1234", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("alice", "x12345", false, ""); err == nil {
		t.Error("duplicate user accepted")
	}
	if err := s.CreateOrg("devs", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrg("devs", ""); err == nil {
		t.Error("duplicate org accepted")
	}
	if err := s.AddOrgMember("devs", "alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOrgMember("devs", "ghost"); err == nil {
		t.Error("adding unknown member accepted")
	}
	if !s.IsOrgMember("devs", "alice") {
		t.Error("membership broken")
	}
	if orgs := s.OrgsOf("alice"); len(orgs) != 1 || orgs[0] != "devs" {
		t.Errorf("OrgsOf: %v", orgs)
	}

	// persistence across reload
	s2, err := Load(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.GetUser("alice"); !ok {
		t.Error("user lost after reload")
	}
	if !s2.IsOrgMember("devs", "alice") {
		t.Error("membership lost after reload")
	}

	if err := s.RemoveOrgMember("devs", "alice"); err != nil {
		t.Fatal(err)
	}
	if s.IsOrgMember("devs", "alice") {
		t.Error("removal failed")
	}
}

func TestDeleteOrgRequiresNoRepos(t *testing.T) {
	s := newStore(t)
	s.CreateOrg("devs", "")
	s.CreateRepo("app", "org:devs", false, "")

	if err := s.DeleteOrg("devs"); err == nil {
		t.Fatal("deleting org with repos allowed")
	}
	if _, ok := s.GetOrg("devs"); !ok {
		t.Fatal("org was deleted despite repos")
	}

	// after removing the repo the org can be deleted
	s.DeleteRepo("devs/app")
	if err := s.DeleteOrg("devs"); err != nil {
		t.Fatalf("deleting empty org failed: %v", err)
	}
	if _, ok := s.GetOrg("devs"); ok {
		t.Error("org not deleted")
	}
}

func TestLegacyGroupMigration(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := osWriteFile(dir+"/"+name, content); err != nil {
			t.Fatal(err)
		}
	}
	write("users.yaml", "- name: admin\n  hash: x\n  admin: true\n")
	write("groups.yaml", "- name: devteam\n  members: [alice]\n")
	write("repos.yaml", "- name: web\n  owner: group:devteam\n")

	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsOrgExists("devteam") {
		t.Error("legacy group not migrated to org")
	}
	rp, _ := s.GetRepo("devteam/web")
	if rp.Owner != "org:devteam" {
		t.Errorf("owner prefix not migrated: %q", rp.Owner)
	}
	if !s.IsOrgMember("devteam", "alice") {
		t.Error("members lost in migration")
	}
}

func TestLastAdminProtection(t *testing.T) {
	s := newStore(t)
	if err := s.SetAdmin("admin", false); err == nil {
		t.Error("demoting last admin allowed")
	}
	if err := s.DeleteUser("admin"); err == nil {
		t.Error("deleting last admin allowed")
	}
	if err := s.CreateUser("root2", "pass1234", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAdmin("admin", false); err != nil {
		t.Errorf("demoting admin with another admin present: %v", err)
	}
}

func TestRepoPermissions(t *testing.T) {
	s := newStore(t)
	s.CreateUser("alice", "pass1234", false, "")
	s.CreateUser("bob", "pass1234", false, "")
	s.CreateOrg("devs", "")
	s.AddOrgMember("devs", "alice")

	if err := s.CreateRepo("app", "org:devs", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepo("web", "user:bob", true, "main"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepo("bad", "org:ghost", false, ""); err == nil {
		t.Error("unknown org owner accepted")
	}
	if err := s.CreateRepo("app", "org:devs", false, ""); err == nil {
		t.Error("duplicate full name accepted")
	}
	if err := s.CreateRepo("../evil", "user:bob", false, ""); err == nil {
		t.Error("bad name accepted")
	}

	app, _ := s.GetRepo("devs/app")
	if !s.CanWrite("alice", app) {
		t.Error("org member cannot write")
	}
	if !s.CanRead("alice", app) {
		t.Error("org member cannot read")
	}
	if s.CanWrite("bob", app) {
		t.Error("outsider can write private repo")
	}
	if s.CanRead("bob", app) {
		t.Error("outsider of another org can read private repo")
	}
	if s.CanRead("", app) {
		t.Error("anonymous can read private repo")
	}
	if !s.CanWrite("admin", app) {
		t.Error("admin cannot write")
	}

	web, _ := s.GetRepo("bob/web")
	if !s.CanRead("", web) {
		t.Error("anonymous cannot read public repo")
	}
	if s.CanWrite("", web) {
		t.Error("anonymous can write public repo")
	}
	if s.CanManage("bob", app) {
		t.Error("non-owner manages org repo")
	}
	if !s.CanManage("bob", web) {
		t.Error("user-owner cannot manage own repo")
	}

	vis := s.VisibleRepos("bob")
	if len(vis) != 1 || vis[0].Name != "web" {
		t.Errorf("visible repos for bob: %+v", vis)
	}
	if vis := s.VisibleRepos(""); len(vis) != 1 {
		t.Errorf("anonymous visible repos: %+v", vis)
	}
	if vis := s.VisibleRepos("alice"); len(vis) != 2 {
		t.Errorf("alice visible repos: %+v", vis)
	}
}

func TestRepoUpdateAndDelete(t *testing.T) {
	s := newStore(t)
	s.CreateRepo("app", "user:admin", false, "")
	if err := s.UpdateRepo("admin/app", func(r *Repo) error {
		r.Public = true
		r.DeployBranch = "main"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rp, _ := s.GetRepo("admin/app")
	if !rp.Public || rp.DeployBranch != "main" {
		t.Errorf("update lost: %+v", rp)
	}
	s.DeleteRepo("admin/app")
	if _, ok := s.GetRepo("admin/app"); ok {
		t.Error("repo not deleted")
	}
	_ = filepath.Join // keep import
}

// 组织名与用户名共享一个命名空间，避免 owner/name 全名出现歧义。
func TestUserOrgNamespaceShared(t *testing.T) {
	s := newStore(t)
	s.CreateUser("carol", "pass1234", false, "")
	if err := s.CreateOrg("carol", ""); err == nil {
		t.Error("org name colliding with user accepted")
	}
	s.CreateOrg("platform", "")
	if err := s.CreateUser("platform", "pass1234", false, ""); err == nil {
		t.Error("user name colliding with org accepted")
	}
}

func TestRepoBySlug(t *testing.T) {
	s := newStore(t)
	s.CreateRepo("app", "user:admin", false, "")
	rp, ok := s.GetRepoBySlug("admin_app")
	if !ok || rp.FullName() != "admin/app" {
		t.Errorf("GetRepoBySlug failed: %+v %v", rp, ok)
	}
	if _, ok := s.GetRepoBySlug("nope"); ok {
		t.Error("unknown slug found")
	}
}

func TestRoles(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("qa1", "pass1234", false, RoleTester); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("dev1", "pass1234", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("bad1", "pass1234", false, "ceo"); err == nil {
		t.Error("invalid role accepted")
	}

	if qa, _ := s.GetUser("qa1"); !qa.Tester() {
		t.Error("qa1 should be tester")
	}
	if dev, _ := s.GetUser("dev1"); dev.Tester() {
		t.Error("empty role defaults to developer")
	}
	// 管理员即使带了 tester 角色字段也永远不受限
	if err := s.CreateUser("boss", "pass1234", true, RoleTester); err != nil {
		t.Fatal(err)
	}
	if boss, _ := s.GetUser("boss"); boss.Tester() {
		t.Error("admin is never restricted by role")
	}

	// 组织成员里的测试角色：可读（站点访问）但绝不可写（部署 = push = 写）
	if err := s.CreateOrg("devs", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOrgMember("devs", "qa1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepo("app", "org:devs", false, ""); err != nil {
		t.Fatal(err)
	}
	rp, ok := s.GetRepo("devs/app")
	if !ok {
		t.Fatal("repo missing")
	}
	if s.CanWrite("qa1", rp) {
		t.Error("tester must have no write access even as org member")
	}
	if !s.CanRead("qa1", rp) {
		t.Error("org-member tester must keep read access for site visits")
	}
	// 直接归属的仓库同样只读
	if err := s.CreateRepo("own", "user:qa1", false, ""); err != nil {
		t.Fatal(err)
	}
	if own, _ := s.GetRepo("qa1/own"); s.CanWrite("qa1", own) {
		t.Error("tester repo owner must have no write access")
	}

	// SetRole
	if err := s.SetRole("qa1", RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	if qa, _ := s.GetUser("qa1"); qa.Tester() {
		t.Error("SetRole did not clear tester")
	}
	if err := s.SetRole("ghost", RoleTester); err == nil {
		t.Error("missing user accepted")
	}
	if err := s.SetRole("dev1", "nope"); err == nil {
		t.Error("invalid role accepted")
	}

	// 持久化
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if qa, _ := s2.GetUser("qa1"); qa.Role != RoleDeveloper {
		t.Errorf("role not persisted: %q", qa.Role)
	}
}

// TestPersistenceReload 回归测试：缺陷/需求/SSH 公钥必须落盘——
// 曾因 saveLocked 未映射这三个文件（yaml.Marshal(nil) → "null"），
// 数据只在内存里，重启即全部丢失。
func TestPersistenceReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("alice", "pass1234", false, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOrg("team", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRepo("shop", "org:team", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBug(&Bug{
		Repo: "team/shop", Title: "bug", Body: "b", Severity: SeverityMajor,
		Status: BugOpen, Reporter: "alice", Assignee: "alice", ID: "bug-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateRequirement(&Requirement{
		Repo: "team/shop", Title: "req", Body: "r", Status: ReqReview,
		Creator: "alice", ID: "req-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSSHKey(&SSHKey{
		User: "alice", Name: "k", Fingerprint: "SHA256:x", Type: "ssh-ed25519",
		Key: "ssh-ed25519 AAAA", ID: "key-1",
	}); err != nil {
		t.Fatal(err)
	}

	// 重新加载：全部数据必须在
	s2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bug, ok := s2.GetBug("bug-1"); !ok || bug.Title != "bug" {
		t.Errorf("bug lost after reload: %+v ok=%v", bug, ok)
	}
	if req, ok := s2.GetRequirement("req-1"); !ok || req.Title != "req" {
		t.Errorf("requirement lost after reload: %+v ok=%v", req, ok)
	}
	if keys := s2.ListSSHKeys("alice"); len(keys) != 1 {
		t.Errorf("ssh keys lost after reload: %d", len(keys))
	}
	if user, ok := s2.SSHKeyUser("SHA256:x"); !ok || user != "alice" {
		t.Errorf("ssh key lookup lost after reload")
	}
	if c := s2.OrgCreator("team"); c != "admin" {
		t.Errorf("org creator lost after reload: %q", c)
	}
}
