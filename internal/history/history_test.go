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

// TestUpdatePublishesProgress：流水线在私有副本上推进状态，Update 按快照
// 覆盖可变字段；快照与调用方切片不共享底层数组。
func TestUpdatePublishesProgress(t *testing.T) {
	s, _ := New(10, "")
	stored := &model.Task{ID: "t1", Repo: "o/r", Branch: "main", Status: model.StatusPending}
	s.Add(stored)

	// 模拟流水线：先拷一份私有副本，再在副本上推进并发布快照。
	local := *stored
	local.Status = model.StatusRunning
	local.Stage = "build-backend"
	local.Steps = make([]model.Step, 1, 8)
	local.Steps[0] = model.Step{Name: "git-sync", OK: true}
	s.Update("t1", &local)

	got, ok := s.Get("t1")
	if !ok {
		t.Fatal("task lost after Update")
	}
	if got.Status != model.StatusRunning || got.Stage != "build-backend" || len(got.Steps) != 1 {
		t.Fatalf("snapshot not published: %+v", got)
	}
	// 调用方继续修改私有副本（含复用 Steps 底层数组）不影响已发布的历史
	local.Steps[0].Name = "mutated"
	local.Steps = append(local.Steps, model.Step{Name: "build-backend", OK: true})
	if again, _ := s.Get("t1"); again.Steps[0].Name != "git-sync" || len(again.Steps) != 1 {
		t.Errorf("store aliases caller slice: %+v", again.Steps)
	}
	// 不可变字段保持不变
	if again, _ := s.Get("t1"); again.Repo != "o/r" || again.Branch != "main" {
		t.Errorf("immutable fields changed: %+v", again)
	}
}

// TestFinishSkippedRecord：队列入队失败（queue full / duplicate）时任务以
// 终止态落历史，不依赖调用方直接改共享 Task 指针。
func TestFinishSkippedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s, err := New(10, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(&model.Task{ID: "d1", Repo: "o/r", Branch: "main", Status: model.StatusPending})
	s.Finish("d1", model.StatusSkipped, "queue full: too many pending deployments")

	got, ok := s.Get("d1")
	if !ok {
		t.Fatal("task missing")
	}
	if got.Status != model.StatusSkipped || got.Error == "" || got.FinishedAt.IsZero() {
		t.Fatalf("finish not applied: %+v", got)
	}
	s2, _ := New(10, path)
	if reloaded := s2.Recent(10); len(reloaded) != 1 || reloaded[0].ID != "d1" {
		t.Errorf("finished task not persisted: %+v", reloaded)
	}
}
