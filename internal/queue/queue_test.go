package queue

import (
	"errors"
	"sync"
	"testing"
	"time"

	"gitwo/internal/model"
)

func mkTask(repo, branch, sha string) *model.Task {
	return &model.Task{ID: model.NewID(), Repo: repo, Branch: branch, SHA: sha}
}

// waitUntil polls cond until true or timeout, for synchronizing with the
// queue's background goroutines.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestSameRepoRunsSerially(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	running, started := 0, 0
	exec := func(task *model.Task) {
		mu.Lock()
		running++
		started++
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
	}
	q := New(4, 10, exec)

	for i := 0; i < 4; i++ {
		if _, err := q.Enqueue(mkTask("o/r", "main", "sha"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return started == 1
	})
	mu.Lock()
	if running != 1 {
		t.Errorf("expected exactly 1 running task for one repo, got %d", running)
	}
	mu.Unlock()
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return q.Pending() == 0 })
}

func TestCrossRepoParallelism(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	running := 0
	peak := 0
	exec := func(task *model.Task) {
		mu.Lock()
		running++
		if running > peak {
			peak = running
		}
		mu.Unlock()
		<-release
		mu.Lock()
		running--
		mu.Unlock()
	}
	q := New(2, 10, exec)

	q.Enqueue(mkTask("o/a", "main", "s1"))
	q.Enqueue(mkTask("o/b", "main", "s2"))
	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return running == 2
	})
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return q.Pending() == 0 })
	mu.Lock()
	defer mu.Unlock()
	if peak < 2 {
		t.Errorf("two repos should run in parallel, peak concurrency = %d", peak)
	}
}

func TestDuplicateCollapsed(t *testing.T) {
	release := make(chan struct{})
	exec := func(task *model.Task) { <-release }
	q := New(1, 10, exec)

	t1 := mkTask("o/r", "main", "shaA")
	if _, err := q.Enqueue(t1); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return q.Pending() == 0 }) // t1 now running

	// duplicate of the running task
	existing, err := q.Enqueue(mkTask("o/r", "main", "shaA"))
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
	if existing != t1 {
		t.Errorf("duplicate should return the existing task, got %+v", existing)
	}
	// duplicate of a pending task
	t2 := mkTask("o/r", "main", "shaB")
	if _, err := q.Enqueue(t2); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(mkTask("o/r", "main", "shaB")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate for pending task, got %v", err)
	}
	// different sha is fine
	if _, err := q.Enqueue(mkTask("o/r", "main", "shaC")); err != nil {
		t.Fatal(err)
	}
	// different branch is fine
	if _, err := q.Enqueue(mkTask("o/r", "dev", "shaA")); err != nil {
		t.Fatal(err)
	}
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return q.Pending() == 0 })
}

func TestQueueCap(t *testing.T) {
	release := make(chan struct{})
	exec := func(task *model.Task) { <-release }
	q := New(1, 2, exec)

	if _, err := q.Enqueue(mkTask("o/r", "main", "s1")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 2*time.Second, func() bool { return q.Pending() == 0 }) // s1 running
	if _, err := q.Enqueue(mkTask("o/r", "main", "s2")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(mkTask("o/r", "main", "s3")); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Enqueue(mkTask("o/r", "main", "s4")); !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	close(release)
	waitUntil(t, 2*time.Second, func() bool { return q.Pending() == 0 })
}
