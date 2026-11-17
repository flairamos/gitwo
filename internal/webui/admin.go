package webui

import (
	"net/http"
	"strings"

	"gitwo/internal/store"
)

// ---- admin dashboard ----

func (u *UI) adminPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	http.Redirect(w, r, "/admin/users", http.StatusFound)
}

func (u *UI) adminUsersPage(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireAdmin(w, r)
	if !ok {
		return
	}
	u.render(w, http.StatusOK, "admin_users", adminUsersData{
		Base:  u.base(r, "用户管理"),
		Users: u.Store.ListUsers(),
		Orgs:  u.Store.ListOrgs(),
		Msg:   r.URL.Query().Get("msg"),
		Self:  usr.Name,
	})
}

func (u *UI) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	password := r.PostFormValue("password")
	admin := r.PostFormValue("admin") == "on"
	role := r.PostFormValue("role")
	if admin {
		role = "" // 管理员不受角色限制
	}
	if err := u.Store.CreateUser(name, password, admin, role); err != nil {
		u.flashRedirect(w, r, "/admin/users", "创建失败: "+err.Error())
		return
	}
	u.Log.Info("user created", "user", name, "admin", admin, "role", role)
	u.flashRedirect(w, r, "/admin/users", "用户 "+name+" 已创建")
}

// adminUserSetRole 修改普通用户的角色（developer / tester）。
func (u *UI) adminUserSetRole(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	role := r.PostFormValue("role")
	if !store.ValidRole(role) {
		u.flashRedirect(w, r, "/admin/users", "未知角色: "+role)
		return
	}
	if err := u.Store.SetRole(name, role); err != nil {
		u.flashRedirect(w, r, "/admin/users", "修改失败: "+err.Error())
		return
	}
	u.Log.Info("role changed", "user", name, "role", role)
	label := "开发者"
	if role == store.RoleTester {
		label = "测试"
	}
	u.flashRedirect(w, r, "/admin/users", name+" 的角色已改为 "+label)
}

func (u *UI) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	if err := u.Store.DeleteUser(name); err != nil {
		u.flashRedirect(w, r, "/admin/users", "删除失败: "+err.Error())
		return
	}
	u.Log.Info("user deleted", "user", name)
	u.flashRedirect(w, r, "/admin/users", "用户已删除")
}

func (u *UI) adminUserReset(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if err := u.Store.SetPassword(name, r.PostFormValue("password")); err != nil {
		u.flashRedirect(w, r, "/admin/users", "重置失败: "+err.Error())
		return
	}
	u.Log.Info("password reset", "user", name)
	u.flashRedirect(w, r, "/admin/users", "密码已重置")
}

func (u *UI) adminUserToggleAdmin(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	want := r.PostFormValue("admin") == "1"
	if err := u.Store.SetAdmin(name, want); err != nil {
		u.flashRedirect(w, r, "/admin/users", "修改失败: "+err.Error())
		return
	}
	u.Log.Info("admin flag changed", "user", name, "admin", want)
	u.flashRedirect(w, r, "/admin/users", "已更新管理员状态")
}

// ---- organizations ----

func (u *UI) adminOrgsPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	repoCount := map[string]int{}
	for _, rp := range u.Store.ListRepos() {
		if kind, name, _ := cutOwner(rp.Owner); kind == "org" {
			repoCount[name]++
		}
	}
	u.render(w, http.StatusOK, "admin_orgs", adminOrgsData{
		Base:      u.base(r, "组织管理"),
		Orgs:      u.Store.ListOrgs(),
		Users:     u.Store.ListUsers(),
		RepoCount: repoCount,
		Msg:       r.URL.Query().Get("msg"),
	})
}

func (u *UI) adminOrgCreate(w http.ResponseWriter, r *http.Request) {
	usr, ok := u.requireAdmin(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if err := u.Store.CreateOrg(name, usr.Name); err != nil {
		u.flashRedirect(w, r, "/admin/orgs", "创建失败: "+err.Error())
		return
	}
	u.Log.Info("org created", "org", name)
	u.flashRedirect(w, r, "/admin/orgs", "组织 "+name+" 已创建，把用户加入组织后即可共享仓库")
}

func (u *UI) adminOrgDelete(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	if err := u.Store.DeleteOrg(name); err != nil {
		u.flashRedirect(w, r, "/admin/orgs", "删除失败: "+err.Error())
		return
	}
	u.Log.Info("org deleted", "org", name)
	u.flashRedirect(w, r, "/admin/orgs", "组织已删除")
}

func (u *UI) adminOrgMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := u.requireAdmin(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	var err error
	switch r.PostFormValue("action") {
	case "add":
		err = u.Store.AddOrgMember(name, strings.TrimSpace(r.PostFormValue("member")))
	case "remove":
		err = u.Store.RemoveOrgMember(name, r.PostFormValue("member"))
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		u.flashRedirect(w, r, "/admin/orgs", "操作失败: "+err.Error())
		return
	}
	u.flashRedirect(w, r, "/admin/orgs", "组织成员已更新")
}

func cutOwner(owner string) (kind, name string, ok bool) {
	k, n, err := strings.Cut(owner, ":")
	return k, n, err && n != ""
}
