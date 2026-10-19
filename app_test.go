package main

import (
	"context"
	"testing"
	"time"
)

func TestPageGone(t *testing.T) {
	now := time.Now()
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	started := now.Add(-5 * time.Minute)
	for _, c := range []struct {
		name      string
		ping, bye int64
		start     time.Time
		want      bool
	}{
		{"pinging", ms(time.Second), 0, started, false},
		{"closed", ms(40 * time.Second), ms(30 * time.Second), started, true},
		{"just closed", ms(10 * time.Second), ms(5 * time.Second), started, false},
		{"reloaded", ms(time.Second), ms(30 * time.Second), started, false},
		{"background tab", ms(5 * time.Minute), 0, started, false},
		{"silent for long", ms(31 * time.Minute), 0, started, true},
		{"starting up", ms(time.Hour), ms(time.Hour), now.Add(-10 * time.Second), false},
	} {
		a := &App{jobs: newJobManager()}
		a.lastPing.Store(c.ping)
		a.lastBye.Store(c.bye)
		if got := a.pageGone(c.start); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// never while a job is running
	a := &App{jobs: newJobManager()}
	a.lastPing.Store(ms(time.Hour))
	release := make(chan struct{})
	a.jobs.Start("test", func(ctx context.Context, j *Job) error { <-release; return nil })
	if a.pageGone(started) {
		t.Error("gone while a job runs")
	}
	close(release)
	a.jobs.Wait(5 * time.Second)
	if !a.pageGone(started) {
		t.Error("not gone after the job")
	}
}
