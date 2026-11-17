// Package queue implements the in-memory deployment queue: a bounded FIFO
// per repo+branch (serial execution within one repo, no working-dir races)
// and a global worker semaphore capping concurrent deployments overall.
package queue

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"gitwo/internal/model"
)

var (
	// ErrBusy means the per-repo pending queue is full.
	ErrBusy = errors.New("queue full: too many pending deployments for this repository")
	// ErrDuplicate means an identical task (same repo/branch/sha) is already
	// queued or running; the existing task is returned instead.
	ErrDuplicate = errors.New("identical task already queued or running")
)

type repoState struct {
	pending []*model.Task
	current *model.Task // task being executed right now
	running bool        // a drain goroutine exists for this repo
}

type Queue struct {
	mu        sync.Mutex
	states    map[string]*repoState
	sem       chan struct{} // global concurrency cap (workers)
	maxQueued int
	exec      func(*model.Task)
}

func New(workers, maxQueued int, exec func(*model.Task)) *Queue {
	if workers < 1 {
		workers = 1
	}
	if maxQueued < 1 {
		maxQueued = 1
	}
	return &Queue{
		states:    make(map[string]*repoState),
		sem:       make(chan struct{}, workers),
		maxQueued: maxQueued,
		exec:      exec,
	}
}

// Enqueue appends the task. Returns (existing, ErrDuplicate) when the same
// repo+branch+sha is already queued/running, (nil, ErrBusy) when the repo's
// queue is full.
func (q *Queue) Enqueue(t *model.Task) (*model.Task, error) {
	k := t.Repo + "@" + t.Branch
	q.mu.Lock()
	st := q.states[k]
	if st == nil {
		st = &repoState{}
		q.states[k] = st
	}
	if st.current != nil && st.current.SHA == t.SHA {
		existing := st.current
		q.mu.Unlock()
		return existing, ErrDuplicate
	}
	for _, p := range st.pending {
		if p.SHA == t.SHA {
			q.mu.Unlock()
			return p, ErrDuplicate
		}
	}
	if len(st.pending) >= q.maxQueued {
		q.mu.Unlock()
		return nil, ErrBusy
	}
	st.pending = append(st.pending, t)
	start := !st.running
	st.running = true
	q.mu.Unlock()
	if start {
		go q.drain(k, st)
	}
	return t, nil
}

// drain runs tasks for one repo, serially, for as long as tasks remain.
func (q *Queue) drain(k string, st *repoState) {
	for {
		q.mu.Lock()
		if len(st.pending) == 0 {
			st.running = false
			q.mu.Unlock()
			return
		}
		t := st.pending[0]
		st.pending = st.pending[1:]
		st.current = t
		q.mu.Unlock()

		q.sem <- struct{}{}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Status = model.StatusFailed
					t.Error = fmt.Sprintf("gitwo internal error: %v", r)
					t.FinishedAt = time.Now()
				}
			}()
			q.exec(t)
		}()
		<-q.sem

		q.mu.Lock()
		st.current = nil
		q.mu.Unlock()
	}
}

// Pending returns the total number of queued (not yet running) tasks.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, st := range q.states {
		n += len(st.pending)
	}
	return n
}

func (q *Queue) Workers() int { return cap(q.sem) }
