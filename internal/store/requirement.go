package store

import (
	"sort"
	"time"

	"gitwo/internal/model"
)

// 需求状态机（仓库级迭代需求文档）：
//
//	任何可读者创建 → pending(待开发) —有写权限者/管理员（代码提交者）→ review(待核查)
//	  ↑                                                        │
//	  └────── 验收不通过（创建者退回）─────────────────────────┘
//	  review  → closed(创建者验收通过，终态)
//	  pending → closed(创建者直接作废，终态)
//
// 内容只有创建者可编辑，其他人只读；需求按仓库严格隔离。
const (
	ReqPending = "pending" // 待开发
	ReqReview  = "review"  // 待核查
	ReqClosed  = "closed"  // 已关闭（终态）
)

func ValidReqStatus(s string) bool {
	return s == ReqPending || s == ReqReview || s == ReqClosed
}

// ReqStatusText 需求状态的中文展示名。
func ReqStatusText(s string) string {
	switch s {
	case ReqPending:
		return "待开发"
	case ReqReview:
		return "待核查"
	case ReqClosed:
		return "已关闭"
	}
	return s
}

// ReqRecord 是需求的状态流转记录（系统写入）。
type ReqRecord struct {
	Author string    `yaml:"author"`
	Body   string    `yaml:"body"`
	Time   time.Time `yaml:"time"`
	Status string    `yaml:"status,omitempty"` // "from>to"，普通记录为空
}

// Requirement 是仓库级需求文档。Repo 为全名 owner/name —— 严格隔离在所属仓库，
// 同组织（对该仓库有读权限）下任何角色可见。
type Requirement struct {
	ID        string      `yaml:"id"`
	Repo      string      `yaml:"repo"`
	Title     string      `yaml:"title"`
	Body      string      `yaml:"body"` // markdown 在线文档
	Status    string      `yaml:"status"`
	Creator   string      `yaml:"creator"`
	Records   []ReqRecord `yaml:"records,omitempty"` // 状态流转记录（系统）
	CreatedAt time.Time   `yaml:"created_at"`
	UpdatedAt time.Time   `yaml:"updated_at"`
	ClosedAt  time.Time   `yaml:"closed_at,omitempty"`
}

// CreateRequirement 落库一条需求。ID 由调用方生成（为空则 store 生成）；
// 时间戳由 store 统一填写。
func (s *Store) CreateRequirement(r *Requirement) error {
	if _, _, ok := SplitFullName(r.Repo); !ok {
		return ErrBadOwner
	}
	if r.Title == "" || r.Body == "" || r.Creator == "" {
		return ErrBadName
	}
	if !ValidReqStatus(r.Status) {
		return ErrBadName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = model.NewID()
	}
	now := time.Now()
	r.CreatedAt, r.UpdatedAt = now, now
	s.Reqs = append(s.Reqs, *r)
	return s.saveLocked("requirements.yaml")
}

// GetRequirement 按 ID 取需求。
func (s *Store) GetRequirement(id string) (Requirement, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.Reqs {
		if r.ID == id {
			return r, true
		}
	}
	return Requirement{}, false
}

// UpdateRequirement 在锁内修改需求并落盘；fn 返回错误则不落盘。
func (s *Store) UpdateRequirement(id string, fn func(*Requirement) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Reqs {
		if s.Reqs[i].ID == id {
			if err := fn(&s.Reqs[i]); err != nil {
				return err
			}
			s.Reqs[i].UpdatedAt = time.Now()
			return s.saveLocked("requirements.yaml")
		}
	}
	return ErrNotFound
}

// ListRequirements 列出某仓库的需求（新→旧）；repo 为空返回全部。
func (s *Store) ListRequirements(repo string) []Requirement {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Requirement
	for _, r := range s.Reqs {
		if repo == "" || r.Repo == repo {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}
