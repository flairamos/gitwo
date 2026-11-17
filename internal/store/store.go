// Package store persists users, organizations and repositories as small
// YAML files under <home>/data. It is the source of truth for the web UI,
// the git server and the deploy pipeline. No database: mutate + save.
//
// Organization model: an org (组织) has members and owns repositories.
// Org members can see and push all repos owned by their org; repos of one
// org are invisible to members of other orgs (public repos excepted).
// An org can only be deleted while it owns no repositories.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gitwo/internal/auth"

	"gopkg.in/yaml.v3"
)

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

var (
	ErrBadName       = errors.New("名称只能包含字母、数字、_ 或 -，且以字母数字开头")
	ErrExists        = errors.New("已存在")
	ErrNotFound      = errors.New("不存在")
	ErrBadOwner      = errors.New("owner 必须是 user:<用户名> 或 org:<组织名>")
	ErrLastAdmin     = errors.New("至少保留一个管理员账户")
	ErrMemberMissing = errors.New("成员不存在")
	ErrOrgHasRepos   = errors.New("组织下仍有仓库，先删除或转移仓库后才能删除组织")
)

// 角色常量。空值与 developer 等价（旧数据兼容：升级后既有用户自动是开发者）。
const (
	RoleDeveloper = "developer"
	RoleTester    = "tester"
)

// ValidRole 报告 role 是否为合法角色值（空 = 开发者）。
func ValidRole(role string) bool {
	return role == "" || role == RoleDeveloper || role == RoleTester
}

type User struct {
	Name      string    `yaml:"name"`
	Hash      string    `yaml:"hash"`
	Admin     bool      `yaml:"admin"`
	Role      string    `yaml:"role,omitempty"` // "" / developer / tester
	CreatedAt time.Time `yaml:"created_at"`
}

// Tester 报告该用户是否是受限的测试角色：只能访问已部署的站点与服务，
// 不能查看源码、克隆仓库或流水线。管理员永远不受限。
func (u User) Tester() bool { return !u.Admin && u.Role == RoleTester }

// Org is an organization: named set of users that owns repositories.
type Org struct {
	Name      string    `yaml:"name"`
	Creator   string    `yaml:"creator,omitempty"` // 组织创建者（可推送受保护主分支）；旧数据为空时按管理员处理
	Members   []string  `yaml:"members"`
	CreatedAt time.Time `yaml:"created_at"`
}

type Repo struct {
	Name         string    `yaml:"name"`
	Owner        string    `yaml:"owner"` // "user:alice" or "org:devs"
	Public       bool      `yaml:"public"`
	DeployBranch string    `yaml:"deploy_branch"` // "" = 任何含 deploy.yaml 的分支
	CreatedAt    time.Time `yaml:"created_at"`
}

// OwnerName returns the bare owner name (user or org) without prefix.
func (r Repo) OwnerName() string {
	_, n, _ := strings.Cut(r.Owner, ":")
	return n
}

// FullName returns "owner/name" — the unique identity of a repo. Owner names
// (users and orgs) share one namespace, so full names are collision-free.
func (r Repo) FullName() string { return r.OwnerName() + "/" + r.Name }

// Slug is the filesystem-safe form of FullName (used for directories).
func (r Repo) Slug() string { return strings.ReplaceAll(r.FullName(), "/", "_") }

// SplitFullName splits "owner/name"; both parts must pass nameRE.
func SplitFullName(full string) (owner, name string, ok bool) {
	o, n, found := strings.Cut(full, "/")
	if !found || o == "" || n == "" || !nameRE.MatchString(o) || !nameRE.MatchString(n) {
		return "", "", false
	}
	return o, n, true
}

type Store struct {
	mu      sync.Mutex
	dir     string
	Users   []User        `yaml:"users"`
	Orgs    []Org         `yaml:"orgs"`
	Repos   []Repo        `yaml:"repos"`
	Bugs    []Bug         `yaml:"bugs"`
	Reqs    []Requirement `yaml:"requirements"`
	SSHKeys []SSHKey      `yaml:"ssh_keys"`
}

// Load reads users/orgs/repos; when no user exists yet it bootstraps the
// default admin/admin account (the UI reminds to change it). Legacy data
// from the previous "group" model (groups.yaml, owner "group:x") is
// migrated transparently.
func Load(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dataDir}
	if err := s.loadFile("users.yaml", &s.Users); err != nil {
		return nil, err
	}
	if err := s.loadFile("orgs.yaml", &s.Orgs); err != nil {
		return nil, err
	}
	if len(s.Orgs) == 0 { // migrate legacy groups.yaml
		var legacy []Org
		if s.loadFile("groups.yaml", &legacy) == nil && len(legacy) > 0 {
			s.Orgs = legacy
			_ = s.saveLocked("orgs.yaml")
		}
	}
	if err := s.loadFile("repos.yaml", &s.Repos); err != nil {
		return nil, err
	}
	if err := s.loadFile("bugs.yaml", &s.Bugs); err != nil {
		return nil, err
	}
	if err := s.loadFile("requirements.yaml", &s.Reqs); err != nil {
		return nil, err
	}
	if err := s.loadFile("sshkeys.yaml", &s.SSHKeys); err != nil {
		return nil, err
	}
	migrated := false
	for i := range s.Repos {
		if strings.HasPrefix(s.Repos[i].Owner, "group:") {
			s.Repos[i].Owner = "org:" + strings.TrimPrefix(s.Repos[i].Owner, "group:")
			migrated = true
		}
	}
	if migrated {
		_ = s.saveLocked("repos.yaml")
	}
	if len(s.Users) == 0 {
		if err := s.CreateUser("admin", "admin", true, ""); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) loadFile(name string, v any) error {
	p := filepath.Join(s.dir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return yaml.Unmarshal(data, v)
}

func (s *Store) saveLocked(name string) error {
	var v any
	switch name {
	case "users.yaml":
		v = s.Users
	case "orgs.yaml":
		v = s.Orgs
	case "repos.yaml":
		v = s.Repos
	case "bugs.yaml":
		v = s.Bugs
	case "requirements.yaml":
		v = s.Reqs
	case "sshkeys.yaml":
		v = s.SSHKeys
	}
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	p := filepath.Join(s.dir, name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ---- users ----

func (s *Store) CreateUser(name, password string, admin bool, role string) error {
	if !nameRE.MatchString(name) {
		return ErrBadName
	}
	if len(password) < 4 {
		return auth.ErrWeakPassword
	}
	if !ValidRole(role) {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.Users {
		if u.Name == name {
			return fmt.Errorf("用户 %w", ErrExists)
		}
	}
	for _, o := range s.Orgs {
		if o.Name == name {
			return fmt.Errorf("用户名已被组织占用: %s", name)
		}
	}
	s.Users = append(s.Users, User{Name: name, Hash: auth.HashPassword(password), Admin: admin, Role: role, CreatedAt: time.Now()})
	sort.Slice(s.Users, func(i, j int) bool { return s.Users[i].Name < s.Users[j].Name })
	return s.saveLocked("users.yaml")
}

// SetRole 修改用户角色（仅管理员入口调用）。管理员账户的角色无实际效果
// （Tester() 对 admin 恒为 false），但允许设置以便角色切换不留脏数据。
func (s *Store) SetRole(name, role string) error {
	if !ValidRole(role) {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Users {
		if s.Users[i].Name == name {
			s.Users[i].Role = role
			return s.saveLocked("users.yaml")
		}
	}
	return ErrNotFound
}

func (s *Store) DeleteUser(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	admins := 0
	for _, u := range s.Users {
		if u.Admin {
			admins++
		}
	}
	found := false
	out := s.Users[:0]
	for _, u := range s.Users {
		if u.Name == name {
			if u.Admin && admins <= 1 {
				return ErrLastAdmin
			}
			found = true
			continue
		}
		out = append(out, u)
	}
	if !found {
		return ErrNotFound
	}
	s.Users = out
	for gi := range s.Orgs {
		m := s.Orgs[gi].Members[:0]
		for _, mem := range s.Orgs[gi].Members {
			if mem != name {
				m = append(m, mem)
			}
		}
		s.Orgs[gi].Members = m
	}
	if err := s.saveLocked("users.yaml"); err != nil {
		return err
	}
	return s.saveLocked("orgs.yaml")
}

func (s *Store) SetPassword(name, password string) error {
	if len(password) < 4 {
		return auth.ErrWeakPassword
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Users {
		if s.Users[i].Name == name {
			s.Users[i].Hash = auth.HashPassword(password)
			return s.saveLocked("users.yaml")
		}
	}
	return ErrNotFound
}

func (s *Store) SetAdmin(name string, admin bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	admins := 0
	for _, u := range s.Users {
		if u.Admin {
			admins++
		}
	}
	for i := range s.Users {
		if s.Users[i].Name == name {
			if s.Users[i].Admin && !admin && admins <= 1 {
				return ErrLastAdmin
			}
			s.Users[i].Admin = admin
			return s.saveLocked("users.yaml")
		}
	}
	return ErrNotFound
}

func (s *Store) GetUser(name string) (User, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getUserLocked(name)
}

func (s *Store) getUserLocked(name string) (User, bool) {
	for _, u := range s.Users {
		if u.Name == name {
			return u, true
		}
	}
	return User{}, false
}

func (s *Store) ListUsers() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, len(s.Users))
	copy(out, s.Users)
	return out
}

func (s *Store) VerifyLogin(name, password string) (User, bool) {
	u, ok := s.GetUser(name)
	if !ok {
		// burn comparable time to avoid user enumeration by timing
		auth.VerifyPassword(password, auth.HashPassword("x"))
		return User{}, false
	}
	if !auth.VerifyPassword(password, u.Hash) {
		return User{}, false
	}
	return u, true
}

// ---- organizations ----

// OrgCreator 返回组织创建者；旧数据无创建者字段时返回空串（规则上按管理员兜底）。
func (s *Store) OrgCreator(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.Orgs {
		if o.Name == name {
			return o.Creator
		}
	}
	return ""
}

func (s *Store) CreateOrg(name, creator string) error {
	if !nameRE.MatchString(name) {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.Orgs {
		if o.Name == name {
			return fmt.Errorf("组织 %w", ErrExists)
		}
	}
	for _, u := range s.Users {
		if u.Name == name {
			return fmt.Errorf("组织名已被用户占用: %s", name)
		}
	}
	s.Orgs = append(s.Orgs, Org{Name: name, Creator: creator, CreatedAt: time.Now()})
	sort.Slice(s.Orgs, func(i, j int) bool { return s.Orgs[i].Name < s.Orgs[j].Name })
	return s.saveLocked("orgs.yaml")
}

// DeleteOrg removes an organization. Refuses while the org still owns
// repositories: move or delete them first.
func (s *Store) DeleteOrg(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, r := range s.Repos {
		if r.Owner == "org:"+name {
			return ErrOrgHasRepos
		}
	}
	out := s.Orgs[:0]
	for _, o := range s.Orgs {
		if o.Name == name {
			found = true
			continue
		}
		out = append(out, o)
	}
	if !found {
		return ErrNotFound
	}
	s.Orgs = out
	return s.saveLocked("orgs.yaml")
}

func (s *Store) AddOrgMember(org, user string) error {
	if _, ok := s.GetUser(user); !ok {
		return ErrMemberMissing
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for gi := range s.Orgs {
		if s.Orgs[gi].Name == org {
			for _, m := range s.Orgs[gi].Members {
				if m == user {
					return fmt.Errorf("成员 %w", ErrExists)
				}
			}
			s.Orgs[gi].Members = append(s.Orgs[gi].Members, user)
			return s.saveLocked("orgs.yaml")
		}
	}
	return ErrNotFound
}

func (s *Store) RemoveOrgMember(org, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for gi := range s.Orgs {
		if s.Orgs[gi].Name == org {
			m := s.Orgs[gi].Members[:0]
			found := false
			for _, mem := range s.Orgs[gi].Members {
				if mem == user {
					found = true
					continue
				}
				m = append(m, mem)
			}
			if !found {
				return ErrNotFound
			}
			s.Orgs[gi].Members = m
			return s.saveLocked("orgs.yaml")
		}
	}
	return ErrNotFound
}

func (s *Store) GetOrg(name string) (Org, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getOrgLocked(name)
}

func (s *Store) getOrgLocked(name string) (Org, bool) {
	for _, o := range s.Orgs {
		if o.Name == name {
			return o, true
		}
	}
	return Org{}, false
}

func (s *Store) ListOrgs() []Org {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Org, len(s.Orgs))
	copy(out, s.Orgs)
	return out
}

func (s *Store) IsOrgMember(org, user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isOrgMemberLocked(org, user)
}

func (s *Store) isOrgMemberLocked(org, user string) bool {
	o, ok := s.getOrgLocked(org)
	if !ok {
		return false
	}
	for _, m := range o.Members {
		if m == user {
			return true
		}
	}
	return false
}

// OrgsOf returns the org names a user belongs to.
func (s *Store) OrgsOf(user string) []string {
	var out []string
	for _, o := range s.ListOrgs() {
		for _, m := range o.Members {
			if m == user {
				out = append(out, o.Name)
				break
			}
		}
	}
	return out
}

// ---- repos ----

func (s *Store) validOwnerLocked(owner string) bool {
	kind, name, ok := strings.Cut(owner, ":")
	if !ok || name == "" {
		return false
	}
	switch kind {
	case "user":
		_, found := s.getUserLocked(name)
		return found
	case "org":
		_, found := s.getOrgLocked(name)
		return found
	}
	return false
}

func (s *Store) IsOrgExists(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.getOrgLocked(name)
	return ok
}

func (s *Store) CreateRepo(name, owner string, public bool, deployBranch string) error {
	if !nameRE.MatchString(name) {
		return ErrBadName
	}
	if deployBranch != "" && !nameRE.MatchString(deployBranch) {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validOwnerLocked(owner) {
		return ErrBadOwner
	}
	_, ownerName, _ := strings.Cut(owner, ":")
	full := ownerName + "/" + name
	for _, r := range s.Repos {
		if r.FullName() == full {
			return fmt.Errorf("仓库 %s %w", full, ErrExists)
		}
	}
	s.Repos = append(s.Repos, Repo{Name: name, Owner: owner, Public: public, DeployBranch: deployBranch, CreatedAt: time.Now()})
	sort.Slice(s.Repos, func(i, j int) bool { return s.Repos[i].FullName() < s.Repos[j].FullName() })
	return s.saveLocked("repos.yaml")
}

// DeleteRepo removes the repo identified by full name ("owner/name").
func (s *Store) DeleteRepo(full string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	out := s.Repos[:0]
	for _, r := range s.Repos {
		if r.FullName() == full {
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		return ErrNotFound
	}
	s.Repos = out
	return s.saveLocked("repos.yaml")
}

// UpdateRepo mutates a repo via fn under lock and persists (full name key).
func (s *Store) UpdateRepo(full string, fn func(*Repo) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Repos {
		if s.Repos[i].FullName() == full {
			if err := fn(&s.Repos[i]); err != nil {
				return err
			}
			return s.saveLocked("repos.yaml")
		}
	}
	return ErrNotFound
}

// GetRepo looks up a repo by full name ("owner/name").
func (s *Store) GetRepo(full string) (Repo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getRepoByFullLocked(full)
}

func (s *Store) getRepoByFullLocked(full string) (Repo, bool) {
	for _, r := range s.Repos {
		if r.FullName() == full {
			return r, true
		}
	}
	return Repo{}, false
}

// GetRepoBySlug looks up a repo by its filesystem slug ("owner_name").
func (s *Store) GetRepoBySlug(slug string) (Repo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Repos {
		if r.Slug() == slug {
			return r, true
		}
	}
	return Repo{}, false
}

func (s *Store) ListRepos() []Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Repo, len(s.Repos))
	copy(out, s.Repos)
	return out
}

// ---- permissions ----
// 归属者（用户本人或组织成员）可读写；admin 全权；public 仓库匿名只读。
// 组织之间相互隔离：A 组织成员看不到 B 组织的私有仓库。

func (s *Store) CanRead(user string, r Repo) bool {
	if r.Public {
		return true
	}
	if user == "" {
		return false
	}
	if u, ok := s.GetUser(user); ok && u.Admin {
		return true
	}
	return s.isOwnerMember(user, r)
}

func (s *Store) CanWrite(user string, r Repo) bool {
	if user == "" {
		return false
	}
	u, ok := s.GetUser(user)
	if !ok {
		return false
	}
	if u.Admin {
		return true
	}
	if u.Tester() {
		// 测试角色即使加入组织也没有任何写权限（部署 = push = 写）
		return false
	}
	return s.isOwnerMember(user, r)
}

// CanManage allows changing repo settings / deleting: admins and direct
// user-owners (org-owned repos are managed by admins only).
func (s *Store) CanManage(user string, r Repo) bool {
	if user == "" {
		return false
	}
	if u, ok := s.GetUser(user); ok && u.Admin {
		return true
	}
	kind, name, _ := strings.Cut(r.Owner, ":")
	return kind == "user" && name == user
}

func (s *Store) isOwnerMember(user string, r Repo) bool {
	kind, name, _ := strings.Cut(r.Owner, ":")
	switch kind {
	case "user":
		return name == user
	case "org":
		return s.IsOrgMember(name, user)
	}
	return false
}

// VisibleRepos returns repos the (possibly anonymous) user may read.
func (s *Store) VisibleRepos(user string) []Repo {
	var out []Repo
	for _, r := range s.ListRepos() {
		if s.CanRead(user, r) {
			out = append(out, r)
		}
	}
	return out
}
