package store

import (
	"sort"
	"time"

	"gitwo/internal/model"
)

// Bug 状态机（仓库级缺陷流转）：
//
//	tester 提交 → open(待修复) —assignee 修复并重新部署→ fixed(待复测)
//	  ↑                                              │
//	  └────── 复测不通过（reporter 重新打开）──────────┘
//	  fixed → closed(reporter 复测通过，永久关闭，终态)
//	  open  → closed(reporter 误报直接关闭，终态)
const (
	BugOpen   = "open"   // 待修复
	BugFixed  = "fixed"  // 待复测（开发已修复并重新部署）
	BugClosed = "closed" // 已关闭（终态：复测通过或误报关闭）
)

// 严重程度。
const (
	SeverityBlocker = "blocker" // 严重：阻塞测试/核心功能不可用
	SeverityMajor   = "major"   // 一般：主流程可用但有明显问题
	SeverityMinor   = "minor"   // 轻微：样式/文案/体验问题
)

func ValidBugStatus(s string) bool {
	return s == BugOpen || s == BugFixed || s == BugClosed
}

func ValidSeverity(s string) bool {
	return s == SeverityBlocker || s == SeverityMajor || s == SeverityMinor
}

// BugStatusText 状态的中文展示名。
func BugStatusText(s string) string {
	switch s {
	case BugOpen:
		return "待修复"
	case BugFixed:
		return "待复测"
	case BugClosed:
		return "已关闭"
	}
	return s
}

// SeverityText 严重程度的中文展示名。
func SeverityText(s string) string {
	switch s {
	case SeverityBlocker:
		return "严重"
	case SeverityMajor:
		return "一般"
	case SeverityMinor:
		return "轻微"
	}
	return s
}

// BugComment 是缺陷下的评论或状态流转记录（Status 非空时为系统记录）。
type BugComment struct {
	Author string    `yaml:"author"`
	Body   string    `yaml:"body"` // markdown
	Time   time.Time `yaml:"time"`
	Status string    `yaml:"status,omitempty"` // "from>to"，普通评论为空
}

// Bug 是仓库级缺陷单。Repo 为全名 owner/name —— 缺陷严格隔离在所属仓库内。
type Bug struct {
	ID        string       `yaml:"id"`
	Repo      string       `yaml:"repo"`
	Title     string       `yaml:"title"`
	Body      string       `yaml:"body"` // 统一 markdown 格式文档（服务端按表单字段组装）
	Severity  string       `yaml:"severity"`
	Status    string       `yaml:"status"`
	Reporter  string       `yaml:"reporter"`
	Assignee  string       `yaml:"assignee"`
	Files     []string     `yaml:"files,omitempty"` // 截图文件名（相对 bug 附件目录）
	Comments  []BugComment `yaml:"comments,omitempty"`
	CreatedAt time.Time    `yaml:"created_at"`
	UpdatedAt time.Time    `yaml:"updated_at"`
	ClosedAt  time.Time    `yaml:"closed_at,omitempty"`
}

// CreateBug 落库一条缺陷。ID 已由调用方生成（handler 需要先按 ID 建附件
// 目录再落库）时保持不变；为空则由 store 生成。时间戳由 store 统一填写。
func (s *Store) CreateBug(b *Bug) error {
	if _, _, ok := SplitFullName(b.Repo); !ok {
		return ErrBadOwner
	}
	if b.Title == "" || b.Body == "" || b.Reporter == "" || b.Assignee == "" {
		return ErrBadName
	}
	if !ValidBugStatus(b.Status) || !ValidSeverity(b.Severity) {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.ID == "" {
		b.ID = model.NewID()
	}
	now := time.Now()
	b.CreatedAt, b.UpdatedAt = now, now
	s.Bugs = append(s.Bugs, *b)
	return s.saveLocked("bugs.yaml")
}

// GetBug 按 ID 取缺陷。
func (s *Store) GetBug(id string) (Bug, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.Bugs {
		if b.ID == id {
			return b, true
		}
	}
	return Bug{}, false
}

// UpdateBug 在锁内修改缺陷并落盘；fn 返回错误则不落盘。UpdatedAt 由 store 统一刷新。
func (s *Store) UpdateBug(id string, fn func(*Bug) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Bugs {
		if s.Bugs[i].ID == id {
			if err := fn(&s.Bugs[i]); err != nil {
				return err
			}
			s.Bugs[i].UpdatedAt = time.Now()
			return s.saveLocked("bugs.yaml")
		}
	}
	return ErrNotFound
}

// PurgeRepoData 删除仓库时级联清理其缺陷与需求（附件目录由调用方删除）。
func (s *Store) PurgeRepoData(repo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	bugs := s.Bugs[:0]
	for _, b := range s.Bugs {
		if b.Repo != repo {
			bugs = append(bugs, b)
		}
	}
	s.Bugs = bugs
	reqs := s.Reqs[:0]
	for _, r := range s.Reqs {
		if r.Repo != repo {
			reqs = append(reqs, r)
		}
	}
	s.Reqs = reqs
	if err := s.saveLocked("bugs.yaml"); err != nil {
		return err
	}
	return s.saveLocked("requirements.yaml")
}

// ListBugs 列出某仓库的缺陷（新→旧）；repo 为空返回全部（管理员视图用）。
func (s *Store) ListBugs(repo string) []Bug {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Bug
	for _, b := range s.Bugs {
		if repo == "" || b.Repo == repo {
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
