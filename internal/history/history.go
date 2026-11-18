// Package history keeps recent deployment tasks: in memory for the API and
// appended to a JSONL file so the list survives gitwo restarts. It is not
// a database — the authoritative, detailed record is always the per-task log
// file on disk.
package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"gitwo/internal/model"
)

type Store struct {
	mu    sync.Mutex
	tasks []*model.Task
	keep  int
	path  string // jsonl file, "" disables persistence
}

// New creates the store and loads previous history from path when present.
func New(keep int, path string) (*Store, error) {
	s := &Store{keep: keep, path: path}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var t model.Task
		if err := json.Unmarshal(line, &t); err != nil {
			continue // tolerate a torn final line from a crash
		}
		s.tasks = append(s.tasks, &t)
	}
	if len(s.tasks) > keep {
		s.tasks = s.tasks[len(s.tasks)-keep:]
	}
	return s, nil
}

// Add registers a freshly created task.
func (s *Store) Add(t *model.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, t)
	if len(s.tasks) > s.keep {
		s.tasks = s.tasks[len(s.tasks)-s.keep:]
	}
}

// Persist appends a finished task to the history file (best effort).
// 入参应是调用方私有的任务副本：存储里的任务由 Update/Finish 加锁更新，
// 不要在这里直接读写共享任务指针。
func (s *Store) Persist(t *model.Task) {
	if t == nil {
		return
	}
	s.persist(*t)
}

// persist 写一行 JSONL（调用方持有任务快照，不在锁内做文件 I/O）。
func (s *Store) persist(t model.Task) {
	if s.path == "" || t.FinishedAt.IsZero() {
		return
	}
	line, err := json.Marshal(t)
	if err != nil {
		return
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Warn("history append failed", "err", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		slog.Warn("history append failed", "err", err)
	}
}

// Update atomically replaces the stored task's progress fields with src's.
// 流水线在私有副本上推进任务状态，然后按阶段发布快照；这样 Web UI 读历史
// （Recent/Get）与流水线写状态不会并发读写同一个 Task。
// 只覆盖会变化的字段：ID/Repo/Branch/Workdir/LogFile 等在 NewTask 后不再修改，
// 保持不变可让调用方安全地读取这些字段（例如去重时读 existing.ID）。
func (s *Store) Update(id string, src *model.Task) {
	if src == nil {
		return
	}
	steps := append([]model.Step(nil), src.Steps...)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.tasks) - 1; i >= 0; i-- {
		if s.tasks[i].ID != id {
			continue
		}
		dst := s.tasks[i]
		dst.Status = src.Status
		dst.Stage = src.Stage
		dst.Error = src.Error
		dst.SHA = src.SHA
		dst.StartedAt = src.StartedAt
		dst.FinishedAt = src.FinishedAt
		dst.Steps = steps
		return
	}
}

// Finish marks a task that never entered the pipeline (queue full, duplicate
// push, or a panic escaping Execute) with a terminal status and appends it to
// the history file. It updates the stored task under the store lock instead of
// letting callers mutate a shared pointer.
func (s *Store) Finish(id, status, errMsg string) {
	var snapshot *model.Task
	s.mu.Lock()
	for i := len(s.tasks) - 1; i >= 0; i-- {
		if s.tasks[i].ID != id {
			continue
		}
		cp := s.tasks[i].Clone()
		cp.Status = status
		cp.Error = errMsg
		cp.FinishedAt = time.Now()
		*s.tasks[i] = cp
		snapshot = &cp
		break
	}
	s.mu.Unlock()
	if snapshot != nil {
		s.persist(*snapshot)
	}
}

// Recent returns up to n finished-or-active tasks, newest first.
func (s *Store) Recent(n int) []model.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.tasks) {
		n = len(s.tasks)
	}
	out := make([]model.Task, 0, n)
	for i := len(s.tasks) - 1; i >= len(s.tasks)-n; i-- {
		out = append(out, s.tasks[i].Clone())
	}
	return out
}

func (s *Store) Get(id string) (model.Task, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.tasks) - 1; i >= 0; i-- {
		if s.tasks[i].ID == id {
			return s.tasks[i].Clone(), true
		}
	}
	return model.Task{}, false
}

// Count returns how many tasks are in the given status (tracked live).
func (s *Store) Count(status string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.tasks {
		if t.Status == status {
			n++
		}
	}
	return n
}
