package main

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"pinkpdf/pdfops"
)

// Result is one output (or one failed input) of a job.
type Result struct {
	Name         string `json:"name"`
	Path         string `json:"path,omitempty"`
	Dir          string `json:"dir,omitempty"`
	Size         int64  `json:"size,omitempty"`
	InSize       int64  `json:"inSize,omitempty"`
	Source       string `json:"source,omitempty"` // input file name
	SourceID     string `json:"sourceId,omitempty"`
	Note         string `json:"note,omitempty"`
	Error        string `json:"error,omitempty"`
	NeedPassword bool   `json:"needPassword,omitempty"`
	IsDir        bool   `json:"isDir,omitempty"`
}

// JobView is the JSON form of a job.
type JobView struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	State    string   `json:"state"` // running, done, error, canceled
	Progress float64  `json:"progress"`
	Message  string   `json:"message"`
	Results  []Result `json:"results"`
	Error    string   `json:"error,omitempty"`
	OutDir   string   `json:"outDir,omitempty"`
	Started  int64    `json:"started"`
	Elapsed  float64  `json:"elapsed"`
}

// Job is a background task with progress.
type Job struct {
	JobView

	mu     sync.Mutex
	cancel context.CancelFunc
	start  time.Time
}

func (j *Job) SetProgress(p float64, msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	j.Progress = p
	if msg != "" {
		j.Message = msg
	}
}

func (j *Job) Add(r Result) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Results = append(j.Results, r)
}

func (j *Job) SetOutDir(d string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.OutDir = d
}

// snapshot returns a copy that is safe to encode.
func (j *Job) snapshot() JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	c := j.JobView
	c.Results = append([]Result{}, j.Results...)
	if c.State == "running" {
		c.Elapsed = time.Since(j.start).Seconds()
	} else {
		c.Elapsed = j.Elapsed
	}
	return c
}

type JobManager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	seq     int
	running sync.WaitGroup
	active  atomic.Int32
}

// Active returns the number of jobs still running.
func (m *JobManager) Active() int { return int(m.active.Load()) }

// Wait blocks until all jobs have finished or the timeout passes.
func (m *JobManager) Wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { m.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

func newJobManager() *JobManager { return &JobManager{jobs: map[string]*Job{}} }

// Start runs fn in the background and returns the job immediately.
func (m *JobManager) Start(kind string, fn func(ctx context.Context, j *Job) error) *Job {
	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.seq++
	now := time.Now()
	j := &Job{JobView: JobView{ID: fmt.Sprintf("j%d", m.seq), Kind: kind, State: "running", Message: "준비 중",
		Started: now.UnixMilli()}, cancel: cancel, start: now}
	m.jobs[j.ID] = j
	// forget old finished jobs
	for id, old := range m.jobs {
		old.mu.Lock()
		finished := old.State != "running"
		old.mu.Unlock()
		if finished && time.Since(old.start) > 2*time.Hour {
			delete(m.jobs, id)
		}
	}
	m.running.Add(1)
	m.active.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.running.Done()
		defer func() {
			if m.active.Add(-1) == 0 {
				pdfops.ClearReservations() // output names only need guarding between running jobs
			}
		}()
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("job %s panic: %v\n%s", j.ID, r, debug.Stack())
					err = fmt.Errorf("예기치 않은 오류가 발생했습니다: %v", r)
				}
			}()
			err = fn(ctx, j)
		}()
		j.mu.Lock()
		defer j.mu.Unlock()
		j.Elapsed = time.Since(j.start).Seconds()
		switch {
		case ctx.Err() != nil:
			j.State = "canceled"
			j.Message = "취소됨"
		case err != nil:
			j.State = "error"
			j.Error = err.Error()
			log.Printf("job %s (%s) failed: %v", j.ID, kind, err)
		default:
			j.State = "done"
			j.Progress = 1
			j.Message = "완료"
		}
		cancel()
	}()
	return j
}

func (m *JobManager) Get(id string) (*Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

func (m *JobManager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		j.cancel()
	}
}
