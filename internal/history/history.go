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
func (s *Store) Persist(t *model.Task) {
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
