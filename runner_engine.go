package capturecontrolwebapp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type doer interface {
	DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error)
}

type phase string

const (
	phaseNoSession  phase = "no_session"
	phaseIdle       phase = "idle"
	phaseStarting   phase = "starting"
	phasePolicy     phase = "policy"
	phaseCorrection phase = "correction"
)

type timings struct {
	heartbeat, hold, call, execStop, policyStart, startPoll, runPoll time.Duration
}

func defaultTimings() timings {
	return timings{
		heartbeat:   2 * time.Second,
		hold:        500 * time.Millisecond,
		call:        5 * time.Second,
		execStop:    15 * time.Second,
		policyStart: 120 * time.Second,
		startPoll:   200 * time.Millisecond,
		runPoll:     time.Second,
	}
}

type session struct {
	ID, Task, Checkpoint string
	FPS                  float64
	Episodes             int
	Counts               map[string]int // "<source>:<outcome>"
}

type engineDeps struct {
	exec, teleop, capture doer // teleop may be nil
	timings               timings
	safeTakeover          bool
	logDir                string
	now                   func() time.Time
	sleep                 func(ctx context.Context, d time.Duration) error
	newID                 func() string
}

type engine struct {
	engineDeps
	opMu sync.Mutex // serializes start_session/start_episode/take_over/label and monitor transitions

	mu        sync.Mutex // guards the fields below
	phase     phase
	sess      *session
	seg       *segment // open segment (starting, policy or correction)
	capturing bool
	lastBeat  time.Time
	lastPoll  time.Time
	startedAt time.Time
	lastErr   string
	opCancel  context.CancelFunc
}

func newEngine(d engineDeps) *engine {
	return &engine{engineDeps: d, phase: phaseNoSession}
}

func (e *engine) heartbeat() {
	e.mu.Lock()
	e.lastBeat = e.now()
	e.mu.Unlock()
}

func (e *engine) startSession(task, checkpoint string, fps float64) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase != phaseNoSession {
		return fmt.Errorf("start_session not allowed in %s", e.phase)
	}
	if task == "" {
		return errors.New("start_session needs a task")
	}
	if fps <= 0 {
		fps = 10
	}
	e.sess = &session{ID: e.newID(), Task: task, Checkpoint: checkpoint, FPS: fps, Counts: map[string]int{}}
	e.phase, e.lastErr = phaseIdle, ""
	return nil
}

func (e *engine) status() map[string]interface{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := map[string]interface{}{
		"episode_runner": true,
		"state":          string(e.phase),
		"session":        nil,
		"episode":        nil,
		"last_error":     e.lastErr,
	}
	if e.sess != nil {
		counts := map[string]interface{}{}
		for k, v := range e.sess.Counts {
			counts[k] = v
		}
		out["session"] = map[string]interface{}{
			"id": e.sess.ID, "task": e.sess.Task, "checkpoint": e.sess.Checkpoint,
			"fps": e.sess.FPS, "episodes": e.sess.Episodes, "counts": counts,
		}
	}
	if e.seg != nil {
		out["episode"] = map[string]interface{}{
			"id": e.seg.Episode, "source": e.seg.Source, "started": e.seg.TStart.UnixMilli(),
		}
	}
	return out
}

// Stubs filled in by later tasks.
func (e *engine) startEpisode(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.phase != phaseIdle {
		return fmt.Errorf("start_episode not allowed in %s", e.phase)
	}
	return errors.New("not implemented")
}

func (e *engine) takeOver(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Errorf("take_over not allowed in %s", e.phase)
}

func (e *engine) label(ctx context.Context, outcome string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Errorf("label not allowed in %s", e.phase)
}
