package webui

import (
	"bytes"
	"html/template"
	"testing"
	"time"

	"gitwo/internal/model"
	"gitwo/internal/store"
)

// TestAllTemplatesRender renders every template with representative data.
// It guards against the "blank page" class of bugs: a template referencing a
// field its data struct does not provide fails mid-execution and the user
// sees a half-rendered page (this really happened with repo sub-pages).
func TestAllTemplatesRender(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	b := base{Title: "标题", User: "admin", Admin: true, Msg: "ok", Host: "127.0.0.1:9527"}

	rp := store.Repo{Name: "demo", Owner: "org:devs", Public: true, DeployBranch: "main", CreatedAt: now}
	page := RepoPage{Repo: rp, CanManage: true, CanWrite: true, HasSite: true, Head: "main"}
	task := model.Task{
		ID: "20260102-030405-abc", Repo: "demo", Branch: "main", SHA: "0123456789abcdef",
		Status: model.StatusSuccess, Stage: "publish-site",
		CreatedAt: now, StartedAt: now, FinishedAt: now,
		Steps: []model.Step{{Name: "git-sync", Start: now, End: now.Add(time.Second), OK: true}},
	}

	sample := map[string]any{
		// shared partials
		"head":       b,
		"nav":        b,
		"flash":      b,
		"foot":       nil,
		"admintabs":  b,
		"statusicon": model.StatusSuccess,
		"plrow":      task,
		// pages
		"login": loginData{Base: b, Error: "e", Username: "u"},
		"me": meData{
			Base: b, User: store.User{Name: "admin", Admin: true, CreatedAt: now},
			Orgs: []string{"devs"}, Error: "",
		},
		"me_keys": meKeysData{
			Base: b,
			Keys: []store.SSHKey{{
				ID: "k1", User: "admin", Name: "laptop", Type: "ssh-ed25519",
				Fingerprint: "SHA256:abc", Key: "ssh-ed25519 AAAA abc",
			}},
		},
		"dash": dashData{
			Base: b, LoggedIn: true, Admin: true,
			Repos:         []repoRow{{Repo: rp, HasSite: true, SiteURL: "/sites/demo/", LastTask: &task}},
			TodoBugsFix:   []store.Bug{{ID: "b1", Repo: "devs/demo", Title: "待修复", Reporter: "qa", Status: store.BugOpen}},
			TodoBugsTest:  []store.Bug{{ID: "b2", Repo: "devs/demo", Title: "待复测", Assignee: "dev", Status: store.BugFixed}},
			TodoReqsCheck: []store.Requirement{{ID: "r1", Repo: "devs/demo", Title: "待核查需求", Creator: "qa", Status: store.ReqReview}},
		},
		"repo_new": repoNewData{Base: b, Owners: []ownerOption{{Value: "user:a", Label: "a"}}, Name: "n", Error: "e"},
		"repo_overview": repoOverviewData{
			Base: b, RepoPage: page,
			Branches:  []branchInfo{{Name: "main", SHA: "abc"}},
			README:    "readme",
			Tasks:     []model.Task{task},
			StaticDir: "/apps/demo/static", Port: 8080,
		},
		"repo_commits": repoCommitsData{
			Base: b, RepoPage: page, Branch: "main",
			Commits: []commitRow{{SHA: "abc", Author: "dev", When: now, Msg: "msg"}},
		},
		"repo_tree": repoTreeData{
			Base: b, RepoPage: page, Branch: "main",
			Crumbs: []crumb{
				{Name: "src", URL: "/repos/devs/demo/tree/main/src"},
				{Name: "main.go", Final: true},
			},
			Entries: []treeEntry{
				{Name: "static", IsDir: true, Size: "-", URL: "/repos/devs/demo/tree/main/static"},
				{Name: "README.md", Size: "1.2 KiB", URL: "/repos/devs/demo/blob/main/README.md"},
			},
			Branches: []branchInfo{{Name: "main", SHA: "abc"}, {Name: "dev", SHA: "def"}},
		},
		"repo_blob": repoBlobData{
			Base: b, RepoPage: page, Branch: "main", Path: "src/main.go",
			Dir: "src", Language: "go",
			Crumbs: []crumb{
				{Name: "src", URL: "/repos/devs/demo/tree/main/src"},
				{Name: "main.go", Final: true},
			},
			Size: 1234, Lines: 42, Content: "package main",
			Branches: []branchInfo{{Name: "main", SHA: "abc"}, {Name: "dev", SHA: "def"}},
		},
		"repo_tester": repoTesterData{
			Base: b, RepoPage: page,
			Bugs: []store.Bug{{
				ID: "20260101-000000-aaa", Repo: "devs/demo", Title: "登录按钮无效",
				Severity: store.SeverityMajor, Status: store.BugOpen,
				Reporter: "tester", Assignee: "dev",
				Body: "## 复现步骤：打开登录页点击登录，按钮无响应",
			}},
			Tasks: []model.Task{task}, StaticDir: "/apps/demo/static", Port: 8080,
		},
		"repo_reqs": repoReqsData{
			Base: b, RepoPage: page,
			Reqs: []store.Requirement{{
				ID: "20260101-000000-bbb", Repo: "devs/demo", Title: "支持导出PDF",
				Status: store.ReqPending, Creator: "qa", Body: "导出 PDF",
			}},
			Pending: 1,
		},
		"req_edit": reqEditData{
			Base: b, RepoPage: page, IsNew: false,
			Req:   store.Requirement{ID: "20260101-000000-bbb", Repo: "devs/demo", Title: "支持导出PDF", Creator: "qa"},
			Title: "支持导出PDF", Body: "导出 PDF",
		},
		"req_detail": reqDetailData{
			Base: b, RepoPage: page,
			Req: store.Requirement{
				ID: "20260101-000000-bbb", Repo: "devs/demo", Title: "支持导出PDF",
				Status: store.ReqReview, Creator: "qa", Body: "导出 PDF",
				Records: []store.ReqRecord{{Author: "dev", Body: "已上线", Status: "pending>review"}},
			},
			RenderedBody: template.HTML("<p>导出 PDF</p>"),
			CanEdit:      true, CanClose: true, CanReject: true,
		},
		"repo_bugs": repoBugsData{
			Base: b, RepoPage: page,
			Bugs: []store.Bug{{
				ID: "20260101-000000-aaa", Repo: "devs/demo", Title: "登录按钮无效",
				Severity: store.SeverityMajor, Status: store.BugOpen,
				Reporter: "tester", Assignee: "dev", Files: []string{"0.png"},
			}},
			Open: 1, Fixed: 0, Closed: 0,
		},
		"bug_new": bugNewData{
			Base: b, RepoPage: page,
			Candidates: []store.User{{Name: "dev"}}, Error: "",
		},
		"bug_detail": bugDetailData{
			Base: b, RepoPage: page,
			Bug: store.Bug{
				ID: "20260101-000000-aaa", Repo: "devs/demo", Title: "登录按钮无效",
				Severity: store.SeverityMajor, Status: store.BugFixed,
				Reporter: "tester", Assignee: "dev",
				Body: "## 复现步骤：打开登录页点击登录，按钮无响应", Files: []string{"0.png"},
				Comments: []store.BugComment{{Author: "dev", Body: "> 状态流转：**待修复** → **待复测**", Status: "open>fixed"}},
			},
			RenderedBody:   template.HTML("<h2>复现步骤</h2>"),
			Comments:       []renderedComment{{BugComment: store.BugComment{Author: "dev", Body: "x", Status: "open>fixed"}, HTML: template.HTML("<p>x</p>")}},
			CanVerify:      true,
			FilesURLPrefix: "/repos/devs/demo/bugs/20260101-000000-aaa/files/",
		},
		"repo_settings": repoSettingsData{Base: b, RepoPage: page},
		"admin_users": adminUsersData{
			Base: b, Users: []store.User{{Name: "admin", Admin: true, CreatedAt: now}},
			Orgs: []store.Org{{Name: "devs", Members: []string{"admin"}}}, Msg: "m", Self: "admin",
		},
		"admin_orgs": adminOrgsData{
			Base: b, Orgs: []store.Org{{Name: "devs", Members: []string{"admin"}}},
			Users: []store.User{{Name: "admin", Admin: true}}, RepoCount: map[string]int{"devs": 1}, Msg: "m",
		},
		"pipelines":       pipelinesData{Base: b, Repo: "devs/demo", Tasks: []model.Task{task}, Running: 1, Pending: 2, Msg: "m"},
		"pipeline_detail": pipelineDetailData{Base: b, Task: task, LogTail: "log"},
		"message":         messageData{Base: b, Title: "t", Body: "b"},
	}

	for _, name := range []string{
		"head", "nav", "flash", "foot", "admintabs", "statusicon", "plrow",
		"login", "me", "me_keys", "dash", "repo_new",
		"repo_overview", "repo_commits", "repo_tree", "repo_blob", "repo_tester", "repo_reqs", "req_edit", "req_detail", "repo_bugs", "bug_new", "bug_detail", "repo_settings",
		"admin_users", "admin_orgs", "pipelines", "pipeline_detail", "message",
	} {
		tmpl := pages.Lookup(name)
		if tmpl == nil {
			t.Fatalf("template %q not defined", name)
		}
		var buf bytes.Buffer
		if err := pages.ExecuteTemplate(&buf, name, sample[name]); err != nil {
			t.Errorf("template %q failed to render: %v", name, err)
			continue
		}
		if buf.Len() == 0 {
			t.Errorf("template %q rendered empty output", name)
		}
	}
}

// TestRepoHeadEscapesMissingFields is a focused variant: repohead is the
// shared header for five pages; rendering it with only the embedded RepoPage
// (the minimal contract) must succeed.
func TestRepoHeadEscapesMissingFields(t *testing.T) {
	page := RepoPage{
		Repo:      store.Repo{Name: "demo", Owner: "user:admin"},
		CanManage: false, CanWrite: false, HasSite: false, Head: "main",
	}
	var buf bytes.Buffer
	if err := pages.ExecuteTemplate(&buf, "repohead", struct {
		base
		RepoPage
	}{base: base{Title: "t"}, RepoPage: page}); err != nil {
		t.Fatalf("repohead with minimal RepoPage failed: %v", err)
	}
}
