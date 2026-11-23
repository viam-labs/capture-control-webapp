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
	heartbeat, hold, call, status, execStop, policyStart, startPoll, runPoll time.Duration
}

func defaultTimings() timings {
	return timings{
		heartbeat:   2 * time.Second,
		hold:        500 * time.Millisecond,
		call:        5 * time.Second,
		status:      time.Second,
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

	mu          sync.Mutex // guards the fields below
	phase       phase
	sess        *session
	seg         *segment // open segment (starting, policy or correction)
	capturing   bool
	lastBeat    time.Time
	lastPoll    time.Time
	startedAt   time.Time
	lastErr     string
	opCancel    context.CancelFunc
	statusFails int // consecutive failed status polls while starting
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
	if !(fps > 0) {
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

// beginOp derives the operation's context and publishes its cancel func so
// abort can interrupt a dependency call in flight. Call with opMu held.
func (e *engine) beginOp(ctx context.Context) (context.Context, func()) {
	c, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	e.opCancel = cancel
	e.mu.Unlock()
	return c, func() {
		e.mu.Lock()
		e.opCancel = nil
		e.mu.Unlock()
		cancel()
	}
}

func call(ctx context.Context, d doer, cmd map[string]interface{}, timeout time.Duration) (map[string]interface{}, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return d.DoCommand(c, cmd)
}

// finish closes the open segment into the log. mu must be held.
func (e *engine) finish(outcome, errMsg string) {
	if e.seg == nil {
		return
	}
	e.seg.TEnd, e.seg.Outcome, e.seg.Error = e.now(), outcome, errMsg
	if err := appendSegment(e.logDir, *e.seg); err != nil {
		e.lastErr = "session log: " + err.Error()
	}
	e.sess.Counts[e.seg.Source+":"+outcome]++
	e.seg = nil
}

func (e *engine) startEpisode(ctx context.Context) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	ctx, done := e.beginOp(ctx)
	defer done()
	e.mu.Lock()
	if e.phase != phaseIdle {
		defer e.mu.Unlock()
		return fmt.Errorf("start_episode not allowed in %s", e.phase)
	}
	e.sess.Episodes++
	e.seg = &segment{Session: e.sess.ID, Episode: e.sess.Episodes, Source: "policy",
		Task: e.sess.Task, Checkpoint: e.sess.Checkpoint, TStart: e.now()}
	e.lastErr = ""
	task := e.sess.Task
	e.mu.Unlock()

	_, err := call(ctx, e.exec, map[string]interface{}{"command": "start", "task": task}, e.timings.call)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.finish("error", err.Error())
		return err
	}
	e.phase, e.statusFails = phaseStarting, 0
	e.startedAt = e.now()
	e.lastBeat, e.lastPoll = e.startedAt, e.startedAt
	return nil
}

// tick is the monitor step. It never blocks on opMu: a busy lock means an
// operation is in flight, so it retries on the next tick.
func (e *engine) tick(ctx context.Context) {
	e.mu.Lock()
	if e.phase != phaseStarting || e.now().Sub(e.lastPoll) < e.timings.startPoll {
		e.mu.Unlock()
		return
	}
	e.lastPoll = e.now()
	seg := e.seg // identifies this episode; a stale poll must not act on a newer one
	e.mu.Unlock()

	st, err := call(ctx, e.exec, map[string]interface{}{"command": "status"}, min(e.timings.call, e.timings.status))
	state, lastErr := "", ""
	e.mu.Lock()
	if e.seg != seg || e.phase != phaseStarting {
		e.mu.Unlock()
		return
	}
	if err != nil {
		e.statusFails++
	} else {
		e.statusFails = 0
		state, _ = st["state"].(string)
		lastErr, _ = st["last_error"].(string)
	}
	fails := e.statusFails
	late := e.now().Sub(e.startedAt) > e.timings.policyStart
	sess := *e.sess
	e.mu.Unlock()

	abortMsg := ""
	switch {
	case err != nil && fails >= 3:
		abortMsg = "executor status unavailable: " + err.Error()
	case state == "error" || state == "stopped" || state == "idle":
		abortMsg = fmt.Sprintf("executor state %s: %s", state, lastErr)
	case state != "running" && late:
		abortMsg = fmt.Sprintf("policy did not start within %s", e.timings.policyStart)
	case state != "running":
		return
	}
	if !e.opMu.TryLock() {
		return
	}
	defer e.opMu.Unlock()
	ctx, done := e.beginOp(ctx)
	defer done()
	e.mu.Lock()
	if e.phase != phaseStarting || e.seg != seg {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	if abortMsg != "" {
		e.abort("error", abortMsg)
		return
	}
	_, err = call(ctx, e.capture, map[string]interface{}{
		"start_capture": true, "frequency_hz": sess.FPS,
		"tags": []string{"task:" + sess.Task, "session:" + sess.ID, fmt.Sprintf("episode:%d", sess.Episodes),
			"source:policy", "checkpoint:" + sess.Checkpoint, fmt.Sprintf("fps:%g", sess.FPS)},
	}, e.timings.call)
	if err != nil {
		e.abort("error", "start capture: "+err.Error())
		return
	}
	e.mu.Lock()
	e.capturing, e.seg.TStart, e.phase = true, e.now(), phasePolicy
	e.mu.Unlock()
}

// stopExecutorAndWait stops the executor and waits (bounded by execStop) until it leaves running.
func (e *engine) stopExecutorAndWait(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, e.timings.execStop)
	defer cancel()
	if _, err := call(ctx, e.exec, map[string]interface{}{"command": "stop"}, e.timings.execStop); err != nil {
		return err
	}
	for {
		st, err := call(ctx, e.exec, map[string]interface{}{"command": "status"}, e.timings.call)
		if err != nil {
			return err
		}
		if s, _ := st["state"].(string); s != "running" {
			return nil
		}
		if err := e.sleep(ctx, 100*time.Millisecond); err != nil {
			return fmt.Errorf("executor did not stop: %w", err)
		}
	}
}

// abort is best-effort and never takes opMu: the caller must hold opMu.
// It leaves the session open and the phase idle. Safe to call twice.
func (e *engine) abort(outcome, msg string) {
	e.mu.Lock()
	ph, capt := e.phase, e.capturing
	e.mu.Unlock()
	if ph != phaseStarting && ph != phasePolicy && ph != phaseCorrection {
		return
	}
	bg := context.Background()
	if ph == phaseStarting || ph == phasePolicy {
		_, _ = call(bg, e.exec, map[string]interface{}{"command": "stop"}, e.timings.execStop)
	}
	if ph == phaseCorrection && e.teleop != nil {
		_, _ = call(bg, e.teleop, map[string]interface{}{"command": "stop"}, e.timings.call)
	}
	if capt {
		_, _ = call(bg, e.capture, map[string]interface{}{"stop_capture": true}, e.timings.call)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.capturing = false
	e.lastErr = ""
	e.finish(outcome, msg)
	if e.lastErr != "" { // finish recorded a log write error: keep it
		msg += "; " + e.lastErr
	}
	e.lastErr, e.phase = msg, phaseIdle
}

func (e *engine) takeOver(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return fmt.Errorf("take_over not allowed in %s", e.phase)
}

func (e *engine) label(ctx context.Context, outcome string) error {
	e.opMu.Lock()
	defer e.opMu.Unlock()
	ctx, done := e.beginOp(ctx)
	defer done()
	e.mu.Lock()
	ph := e.phase
	e.mu.Unlock()
	if ph != phasePolicy {
		return fmt.Errorf("label not allowed in %s", ph)
	}
	if outcome != "success" && outcome != "failure" {
		return fmt.Errorf("label outcome must be success or failure, got %q", outcome)
	}
	if err := e.stopExecutorAndWait(ctx); err != nil {
		e.abort("error", "stop executor: "+err.Error())
		return err
	}
	if _, err := call(ctx, e.capture, map[string]interface{}{"stop_capture": true}, e.timings.call); err != nil {
		e.abort("error", "stop capture: "+err.Error())
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.capturing = false
	e.finish(outcome, "")
	e.phase = phaseIdle
	return nil
}
