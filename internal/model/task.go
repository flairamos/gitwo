// Package model defines the deployment task shared by every stage of
// gitwo: queue, pipeline, history store and HTTP API.
package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Task lifecycle states.
const (
	StatusPending = "pending" // queued, waiting for a worker
	StatusRunning = "running" // pipeline executing
	StatusSuccess = "success"
	StatusFailed  = "failed"
	StatusSkipped = "skipped" // rejected without running (e.g. queue full)
)

// Step is one recorded pipeline stage (git sync, build, deploy, ...).
type Step struct {
	Name   string    `json:"name"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	OK     bool      `json:"ok"`
	Detail string    `json:"detail,omitempty"`
}

// Task is a single deployment attempt.
type Task struct {
	ID         string    `json:"id"`
	Repo       string    `json:"repo"` // owner/name, as registered
	Branch     string    `json:"branch"`
	SHA        string    `json:"sha"`
	CloneURL   string    `json:"-"`
	Workdir    string    `json:"workdir,omitempty"`
	Status     string    `json:"status"`
	Stage      string    `json:"stage,omitempty"`
	Error      string    `json:"error,omitempty"`
	LogFile    string    `json:"log_file,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Steps      []Step    `json:"steps,omitempty"`
}

func (t Task) ShortSHA() string {
	if len(t.SHA) > 8 {
		return t.SHA[:8]
	}
	return t.SHA
}

// Slug returns a filesystem-safe identifier for the repo, used for log dirs.
func (t Task) Slug() string { return strings.ReplaceAll(t.Repo, "/", "_") }

func (t Task) Duration() time.Duration {
	if t.StartedAt.IsZero() {
		return 0
	}
	end := t.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(t.StartedAt)
}

// Clone returns a shallow copy safe to hand out while the task keeps running.
func (t Task) Clone() Task {
	t.Steps = append([]Step(nil), t.Steps...)
	return t
}

// NewID returns a timestamped, collision-resistant task id that is safe to
// use as a file name.
func NewID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}
