package history

import (
	"path/filepath"
	"testing"
	"time"

	"gitwo/internal/model"
)

func finishedTask(id string) *model.Task {
	now := time.Now()
	return &model.Task{
		ID: id, Repo: "o/r", Branch: "main", SHA: "abc",
		Status: model.StatusSuccess, CreatedAt: now, StartedAt: now, FinishedAt: now,
	}
}

func TestPersistAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, err := New(10, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(finishedTask("t1"))
	s.Persist(finishedTask("t1"))
	s.Add(finishedTask("t2"))
	s.Persist(finishedTask("t2"))

	s2, err := New(10, path)
	if err != nil {
		t.Fatal(err)
	}
	recent := s2.Recent(10)
	if len(recent) != 2 {
		t.Fatalf("reload got %d tasks, want 2", len(recent))
	}
	if recent[0].ID != "t2" || recent[1].ID != "t1" {
		t.Errorf("newest-first ordering broken: %v, %v", recent[0].ID, recent[1].ID)
	}
	got, ok := s2.Get("t1")
	if !ok || got.Repo != "o/r" {
		t.Errorf("Get failed: %+v ok=%v", got, ok)
	}
	if _, ok := s2.Get("nope"); ok {
		t.Error("Get found nonexistent task")
	}
}

func TestKeepCapacity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, err := New(3, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		tk := finishedTask(id)
		s.Add(tk)
		s.Persist(tk)
	}
	if len(s.Recent(100)) != 3 {
		t.Error("memory capacity not enforced")
	}
	s2, _ := New(3, path)
	if got := s2.Recent(100); len(got) != 3 || got[0].ID != "e" || got[2].ID != "c" {
		t.Errorf("reloaded history wrong: %+v", got)
	}
}

func TestCountRunning(t *testing.T) {
	s, _ := New(10, "")
	r := finishedTask("r")
	r.Status = model.StatusRunning
	s.Add(r)
	s.Add(finishedTask("d1"))
	if s.Count(model.StatusRunning) != 1 {
		t.Error("running count wrong")
	}
	if s.Count(model.StatusSuccess) != 1 {
		t.Error("success count wrong")
	}
}
