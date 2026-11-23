# Episode Runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A second model in this module, `viam-labs:capture-control:episode-runner`, that runs policy episodes on a
physical robot, labels each one, hands control to teleop on demand and captures the correction, with a Policy session
UI in the web app.

**Spec:** `docs/superpowers/specs/2026-11-23-episode-runner-design.md`. Read it first: the executor state table,
the state machine, the takeover order, the concurrency rules and the safety section are binding.

**Architecture:**
- Go, root package `capturecontrolwebapp`.
  - `runner_engine.go`: the state machine against a tiny `doer` interface (anything with `DoCommand`), with an
    injectable clock and sleep, so tests use fakes and run instantly.
  - `runner_log.go`: the JSONL session log.
  - `runner.go`: the Viam resource (config, validation, dependency lookup, `DoCommand` dispatch, the monitor
    goroutine, `Close`).
- TypeScript: `src/runner.ts` (pure helpers: status detection, log → tag mapping, session summary) with
  `src/runner.test.ts`; `src/PolicySession.svelte` (operator UI); small changes to `Recorder.svelte`, `Review.svelte`,
  `review.ts` and `data.ts`.

**Tech Stack:** Go 1.26, `go.viam.com/rdk` v1.14 (`generic.FromProvider`, `sensor.FromProvider`,
`resource.AlwaysRebuild`); Svelte 5 runes, `@viamrobotics/sdk` (`GenericServiceClient`), `node:test`.

**Facts the engineer needs:**
- **Executor (vla:controller)** `doCommand`: `{"command":"start","task":"…"}` acks at once and sets state
  `waiting_for_policy`; it becomes `running` once the policy is warm (seconds to minutes). `{"command":"stop"}`;
  `{"command":"status"}` → `{"state": "idle"|"waiting_for_policy"|"running"|"error"|"stopped", "last_error": "…", …}`.
  `last_error` can be set while `running`: only `state` decides failure.
- **Teleop (`devrel:so101:teleop`)**: `{"command":"start"|"stop"|"status"}`; status → `{"running": bool,
  "last_error": "…"}`. Start/stop are idempotent.
- **Capture sensor**: `{"start_capture": true, "frequency_hz": <fps>, "tags": [...]}`, `{"stop_capture": true}`.
- **Go registration**: `cmd/module/main.go` calls `module.ModularMain(resource.APIModel{…})`; add a second
  `APIModel`. Config validators in rdk v1.14 have the signature
  `Validate(path string) (required []string, optional []string, err error)`.
- **Module data dir**: `$VIAM_MODULE_DATA` is set by viam-server for modules (fall back to `os.TempDir()` when empty).
- **Tests**: `go test ./...` and `npm test` (`node --test src/*.test.ts`); `npm run check` (svelte-check);
  `make test` runs both (and builds `dist/`, which `go:embed` needs: run `npm run build` once before `go test` if
  `dist/` is missing).
- Commits: imperative, no prefix, ending with the attribution lines the controller gives. Work on branch
  `episode-runner`; never push.

## File map

| File | Responsibility |
|---|---|
| `runner_engine.go` | States, transitions, takeover order, monitor tick, safety stops |
| `runner_log.go` | Append and read the session's JSONL log |
| `runner.go` | Resource: config + validation, dependencies, `DoCommand`, monitor goroutine, `Close` |
| `runner_engine_test.go` | State-machine tests with fakes |
| `cmd/module/main.go`, `meta.json` | Register and declare the new model |
| `src/runner.ts`, `src/runner.test.ts` | Status detection, log → outcome tags, summary |
| `src/PolicySession.svelte` | Operator UI for a policy session |
| `src/Recorder.svelte` | Discover the runner; offer Policy session; mount `PolicySession` |
| `src/review.ts`, `src/data.ts`, `src/Review.svelte` | `source`/`outcome` parsing, filters, outcome tagging |
| `README.md` | Episode runner section |

---

### Task 1 ★ (user, hardware): teleop pose check

Not for an implementer. On an SO-101 with `devrel:so101:teleop`: teleop off, move the follower away from the leader
by hand or with a saved pose, then `{"command":"start"}`. Does the follower hold its pose (good), or jump to the
leader (bad)? Record the result in the README task (Task 9). The code ships with `teleop_safe_takeover` defaulting to
**false**, so `take_over` is refused until an operator sets it true for a device that passed this check.

---

### Task 2: Engine core — states, session, status, heartbeat

**Files:**
- Create: `runner_engine.go`, `runner_log.go`
- Test: `runner_engine_test.go`

- [ ] **Step 1: Write the failing tests** `runner_engine_test.go` (fakes shared by later tasks):

```go
package capturecontrolwebapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeDoer records commands and answers like the real dependency.
type fakeDoer struct {
	mu      sync.Mutex
	kind    string // "executor", "teleop", "capture"
	calls   []map[string]interface{}
	state   string // executor state
	running bool   // teleop running
	failOn  string // command name that returns an error
	block   chan struct{}
}

func cmdName(cmd map[string]interface{}) string {
	if c, ok := cmd["command"].(string); ok {
		return c
	}
	for k := range cmd {
		return k // start_capture / stop_capture
	}
	return ""
}

func (f *fakeDoer) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	name, block := cmdName(cmd), f.block
	if name == f.failOn {
		f.mu.Unlock()
		return nil, errors.New("fail " + name)
	}
	switch f.kind + "/" + name {
	case "executor/start":
		f.state = "waiting_for_policy"
	case "executor/stop":
		f.state = "stopped"
	case "teleop/start":
		f.running = true
	case "teleop/stop":
		f.running = false
	}
	out := map[string]interface{}{"state": f.state, "running": f.running, "last_error": ""}
	f.mu.Unlock()
	if block != nil && name != "status" {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return out, nil
}

func (f *fakeDoer) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if n := cmdName(c); n != "status" {
			out = append(out, n)
		}
	}
	return out
}

func (f *fakeDoer) set(state string) { f.mu.Lock(); f.state = state; f.mu.Unlock() }

type fakeClock struct{ mu sync.Mutex; t time.Time }

func (c *fakeClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rig struct {
	e                     *engine
	exec, teleop, capture *fakeDoer
	clock                 *fakeClock
	slept                 []time.Duration
}

func newRig(t *testing.T, safeTakeover bool) *rig {
	r := &rig{
		exec:    &fakeDoer{kind: "executor", state: "idle"},
		teleop:  &fakeDoer{kind: "teleop"},
		capture: &fakeDoer{kind: "capture"},
		clock:   &fakeClock{t: time.Unix(1_800_000_000, 0)},
	}
	r.e = newEngine(engineDeps{
		exec: r.exec, teleop: r.teleop, capture: r.capture,
		timings:      defaultTimings(),
		safeTakeover: safeTakeover,
		logDir:       t.TempDir(),
		now:          r.clock.now,
		sleep:        func(ctx context.Context, d time.Duration) error { r.slept = append(r.slept, d); return ctx.Err() },
		newID:        func() string { return "s1" },
	})
	return r
}

func TestNoSessionStatus(t *testing.T) {
	r := newRig(t, true)
	st := r.e.status()
	if st["episode_runner"] != true || st["state"] != "no_session" || st["session"] != nil || st["episode"] != nil {
		t.Fatalf("status %v", st)
	}
	if _, has := st["running"]; has {
		t.Fatal("status must not carry a boolean running (teleop discovery would match it)")
	}
}

func TestStartSessionAndRefusals(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	if err := r.e.startEpisode(ctx); err == nil {
		t.Fatal("start_episode without a session must be refused")
	}
	if err := r.e.startSession("pick the block", "pi05-v1", 10); err != nil {
		t.Fatal(err)
	}
	if err := r.e.startSession("again", "x", 10); err == nil {
		t.Fatal("a second start_session must be refused")
	}
	st := r.e.status()
	if st["state"] != "idle" {
		t.Fatalf("state %v", st["state"])
	}
	if err := r.e.takeOver(ctx); err == nil {
		t.Fatal("take_over in idle must be refused")
	}
	if err := r.e.label(ctx, "success"); err == nil {
		t.Fatal("label in idle must be refused")
	}
}

func TestHeartbeatUpdatesLastBeat(t *testing.T) {
	r := newRig(t, true)
	r.clock.advance(time.Second)
	r.e.heartbeat()
	if !r.e.lastBeat.Equal(r.clock.now()) {
		t.Fatal("heartbeat must record the current time")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./... -run 'TestNoSession|TestStartSession|TestHeartbeat'`
Expected: build failure (`newEngine` undefined).

- [ ] **Step 3: Implement** `runner_log.go`:

```go
package capturecontrolwebapp

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// segment is one captured (or attempted) run: a policy run or a correction.
type segment struct {
	Session    string    `json:"session"`
	Episode    int       `json:"episode"`
	Source     string    `json:"source"` // "policy" | "correction"
	Task       string    `json:"task"`
	Checkpoint string    `json:"checkpoint"`
	TStart     time.Time `json:"t_start"`
	TEnd       time.Time `json:"t_end"`
	Outcome    string    `json:"outcome"` // success | failure | takeover | error | stopped
	Error      string    `json:"error"`
}

func logPath(dir, session string) string { return filepath.Join(dir, session+".jsonl") }

func appendSegment(dir string, s segment) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath(dir, s.Session), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func readSegments(dir, session string) ([]segment, error) {
	f, err := os.Open(logPath(dir, session))
	if os.IsNotExist(err) {
		return []segment{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []segment{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var s segment
		if json.Unmarshal(sc.Bytes(), &s) == nil {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}
```

`runner_engine.go` (this task: types, constructor, session, status, heartbeat; later tasks add the rest):

```go
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
	timings              timings
	safeTakeover         bool
	logDir               string
	now                  func() time.Time
	sleep                func(ctx context.Context, d time.Duration) error
	newID                func() string
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
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./... -run 'TestNoSession|TestStartSession|TestHeartbeat' -v` (run `npm run build` first if `dist/`
is missing).
Expected: PASS. Then `go vet ./...`.

- [ ] **Step 5: Commit**

```bash
git add runner_engine.go runner_log.go runner_engine_test.go
git commit -m "Episode runner: engine states, session, status, heartbeat, session log"
```

---

### Task 3: Policy episodes — start, starting → policy, label

**Files:** Modify `runner_engine.go`; extend `runner_engine_test.go`.

Behaviour (spec "Episode state machine" and "Safety"):
- `startEpisode(ctx)` (opMu): only in idle with a session. Increments `sess.Episodes`; opens a policy segment
  (`Source: "policy"`, episode number, task, checkpoint); sends `executor.start(task)` bounded by `timings.call`. On
  error: log the segment with `outcome: "error"`, `Error: <err>`, stay idle, return the error. On success:
  `phase = starting`, `startedAt = lastBeat = lastPoll = now()`. Returns without waiting for `running`.
- `tick(ctx)` (called by the monitor goroutine every 50 ms; tests call it directly). In `starting`, at most once per
  `timings.startPoll`: read executor `status`.
  - `waiting_for_policy`: if `now - startedAt > policyStart` → `abort("error", "policy did not start within …")`.
  - `running`: take opMu, re-check the phase is still `starting`, `start_capture` with `frequency_hz = fps` and tags
    `task:<task>`, `session:<id>`, `episode:<n>`, `source:policy`, `checkpoint:<checkpoint>`, `fps:<fps formatted
    with %g>`; set `capturing`, `seg.TStart = now()`, `phase = policy`. A capture failure → `abort("error", …)`.
  - `error`, `stopped`, `idle` → `abort("error", "executor state <s>: <last_error>")`.
- `label(ctx, outcome)` (opMu) in `policy`: outcome must be `success` or `failure`. `stopExecutorAndWait` (send
  `stop` bounded by `execStop`, then poll `status` every 100 ms via `sleep` until state ≠ `running`, total bound
  `execStop`), `stop_capture` (bounded by `call`), finish the segment with the outcome, `phase = idle`. Any failure →
  `abort("error", …)` and return the error.
- `finish(outcome, errMsg)` (mu held): set `TEnd`, `Outcome`, `Error`; `appendSegment`; `Counts[source+":"+outcome]++`;
  `seg = nil`.
- `abort(outcome, msg)`: best-effort, never blocks forever: with fresh contexts bounded by the timings, send executor
  `stop` (if the phase was starting/policy), teleop `stop` (if correction), `stop_capture` (if capturing); finish the
  open segment with `outcome`; set `lastErr = msg`; `phase = idle` (the session stays open). Safe to call twice.

- [ ] **Step 1: Write the failing tests** (append):

```go
func startedSession(t *testing.T, r *rig) {
	t.Helper()
	if err := r.e.startSession("pick", "pi05-v1", 10); err != nil {
		t.Fatal(err)
	}
}

func TestStartEpisodeWaitsForRunningBeforeCapture(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	if err := r.e.startEpisode(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.e.status()["state"]; got != "starting" {
		t.Fatalf("state %v", got)
	}
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(ctx) // executor still waiting_for_policy
	if len(r.capture.names()) != 0 {
		t.Fatal("capture must not start before the executor runs")
	}
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(ctx)
	if got := r.e.status()["state"]; got != "policy" {
		t.Fatalf("state %v", got)
	}
	c := r.capture.calls[0]
	tags := c["tags"].([]string)
	want := []string{"task:pick", "session:s1", "episode:1", "source:policy", "checkpoint:pi05-v1", "fps:10"}
	if len(tags) != len(want) {
		t.Fatalf("tags %v", tags)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("tags %v", tags)
		}
	}
}

func TestPolicyStartTimeout(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	_ = r.e.startEpisode(ctx)
	for i := 0; i < 700; i++ { // 140 s of heartbeats + polls
		r.clock.advance(200 * time.Millisecond)
		r.e.heartbeat()
		r.e.tick(ctx)
	}
	st := r.e.status()
	if st["state"] != "idle" || st["last_error"] == "" {
		t.Fatalf("status %v", st)
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) != 1 || segs[0].Outcome != "error" {
		t.Fatalf("log %v", segs)
	}
}

func TestLabelSuccessStopsExecutorThenCapture(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	_ = r.e.startEpisode(ctx)
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(ctx)
	if err := r.e.label(ctx, "success"); err != nil {
		t.Fatal(err)
	}
	if got := r.exec.names(); got[len(got)-1] != "stop" {
		t.Fatalf("executor calls %v", got)
	}
	if got := r.capture.names(); len(got) != 2 || got[1] != "stop_capture" {
		t.Fatalf("capture calls %v", got)
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) != 1 || segs[0].Outcome != "success" || segs[0].Source != "policy" {
		t.Fatalf("log %v", segs)
	}
	st := r.e.status()
	if st["state"] != "idle" || st["session"].(map[string]interface{})["counts"].(map[string]interface{})["policy:success"] != 1 {
		t.Fatalf("status %v", st)
	}
}

func TestExecutorRefusesStart(t *testing.T) {
	r := newRig(t, true)
	r.exec.failOn = "start"
	startedSession(t, r)
	if err := r.e.startEpisode(context.Background()); err == nil {
		t.Fatal("expected the refusal")
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) != 1 || segs[0].Outcome != "error" || r.e.status()["state"] != "idle" {
		t.Fatalf("log %v", segs)
	}
}

func TestExecutorErrorDuringStartingAborts(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	_ = r.e.startEpisode(ctx)
	r.exec.set("error")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(ctx)
	if r.e.status()["state"] != "idle" || len(r.capture.names()) != 0 {
		t.Fatal("an executor error while starting must abort without capturing")
	}
}
```

- [ ] **Step 2: Run to verify they fail**: `go test ./... -run 'TestStartEpisode|TestPolicyStart|TestLabelSuccess|TestExecutorRefuses|TestExecutorErrorDuring'` → FAIL.
- [ ] **Step 3: Implement** the behaviour above in `runner_engine.go`, replacing the `startEpisode` and `label`
  stubs and adding `tick`, `finish`, `abort`, `stopExecutorAndWait` and a `call(ctx, d doer, cmd, timeout)` helper
  (`context.WithTimeout` around `d.DoCommand`). Never hold `mu` across a dependency call: copy what you need, unlock,
  call, re-lock and re-check the phase. `tags` must be a `[]string` in the capture command (tests assert it).
- [ ] **Step 4: Run all engine tests**: `go test ./... -v` → PASS.
- [ ] **Step 5: Commit**: `git commit -am "Episode runner: policy episodes, starting until running, labels"`

---

### Task 4: Takeover and corrections

**Files:** Modify `runner_engine.go`; extend tests.

Behaviour (spec "take_over, in this order"):
- `takeOver(ctx)` (opMu) only in `policy`. Refused with an error (nothing changes) when `teleop == nil` or
  `!safeTakeover` (message names `teleop_safe_takeover`).
- Order: (1) `stopExecutorAndWait`; (2) `stop_capture`, finish the policy segment with `outcome: "takeover"`;
  (3) open a correction segment (same episode, `Source: "correction"`), `start_capture` with tags `task:`, `session:`,
  `episode:<same n>`, `source:correction`, `checkpoint:`, `fps:`; `seg.TStart = now()`; (4) `sleep(ctx, timings.hold)`;
  (5) teleop `start`; `phase = correction`, `lastBeat = now()`. Any failure → `abort("error", …)`, return the error.
- `label(ctx, outcome)` in `correction`: teleop `stop`, `stop_capture`, finish with the outcome, `phase = idle`.

- [ ] **Step 1: Failing tests:**

```go
func toPolicy(t *testing.T, r *rig) {
	t.Helper()
	startedSession(t, r)
	if err := r.e.startEpisode(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(context.Background())
}

func TestTakeoverOrder(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	toPolicy(t, r)
	if err := r.e.takeOver(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.exec.names(); got[len(got)-1] != "stop" {
		t.Fatalf("executor %v", got)
	}
	if got := r.capture.names(); len(got) != 3 || got[1] != "stop_capture" || got[2] != "start_capture" {
		t.Fatalf("capture %v", got)
	}
	tags := r.capture.calls[2]["tags"].([]string)
	if tags[2] != "episode:1" || tags[3] != "source:correction" {
		t.Fatalf("correction tags %v", tags)
	}
	if len(r.slept) == 0 || r.slept[len(r.slept)-1] != 500*time.Millisecond {
		t.Fatalf("hold %v", r.slept)
	}
	if got := r.teleop.names(); len(got) != 1 || got[0] != "start" {
		t.Fatalf("teleop %v", got)
	}
	if r.e.status()["state"] != "correction" {
		t.Fatal("state must be correction")
	}
	if err := r.e.label(ctx, "success"); err != nil {
		t.Fatal(err)
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) != 2 || segs[0].Outcome != "takeover" || segs[1].Source != "correction" || segs[1].Outcome != "success" {
		t.Fatalf("log %v", segs)
	}
	if got := r.teleop.names(); got[len(got)-1] != "stop" {
		t.Fatalf("teleop %v", got)
	}
}

func TestTakeoverRefusedWithoutSafeTeleop(t *testing.T) {
	r := newRig(t, false)
	toPolicy(t, r)
	if err := r.e.takeOver(context.Background()); err == nil {
		t.Fatal("take_over must be refused when teleop_safe_takeover is false")
	}
	if r.e.status()["state"] != "policy" || len(r.teleop.names()) != 0 {
		t.Fatal("a refused take_over must change nothing")
	}
}
```

Also add `TestTakeoverRefusedWithoutTeleop` (rig with `e.teleop = nil` before the session) asserting the same.

- [ ] **Step 2:** run → FAIL. **Step 3:** implement. **Step 4:** `go test ./... -v` → PASS.
- [ ] **Step 5: Commit**: `git commit -am "Episode runner: takeover in the spec's order, correction labels"`

---

### Task 5: Safety — heartbeat watchdog, run polling, stop pre-emption

**Files:** Modify `runner_engine.go`; extend tests.

Behaviour:
- In `tick`: if the phase is `starting`, `policy` or `correction` and `now - lastBeat > timings.heartbeat` →
  `abort("stopped", "heartbeat lost")`. Check this first, before polling.
- In `policy`, at most once per `timings.runPoll`: executor `status`; state ≠ `running` → `abort("error", …)`.
  `last_error` with state `running` changes nothing.
- `stop(ctx)`: does not wait for `opMu` first: under `mu`, call `opCancel` if set (every opMu operation stores its
  cancel func in `opCancel` for its duration and passes the derived ctx to its dependency calls and `sleep`). Then take
  `opMu`, `abort("stopped", "stopped by operator")` if an episode is open, then `sess = nil`, `phase = no_session`.
  Idempotent: a second `stop` with no session returns nil.
- A cancelled operation must end in `abort("stopped", …)` (not `"error"`): check `ctx.Err()` when a step fails.

- [ ] **Step 1: Failing tests:**

```go
func TestHeartbeatLossStopsPolicy(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	toPolicy(t, r)
	r.clock.advance(2100 * time.Millisecond)
	r.e.tick(ctx)
	if r.e.status()["state"] != "idle" {
		t.Fatal("heartbeat loss must stop the episode")
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if segs[len(segs)-1].Outcome != "stopped" {
		t.Fatalf("log %v", segs)
	}
	if got := r.exec.names(); got[len(got)-1] != "stop" {
		t.Fatalf("executor %v", got)
	}
}

func TestHeartbeatLossDuringStarting(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	_ = r.e.startEpisode(ctx)
	r.clock.advance(2100 * time.Millisecond)
	r.e.tick(ctx)
	if r.e.status()["state"] != "idle" {
		t.Fatal("heartbeat loss while starting must stop the executor")
	}
}

func TestLastErrorWhileRunningIsTolerated(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	toPolicy(t, r)
	// fake reports last_error "" by default; state running is what matters
	r.clock.advance(1100 * time.Millisecond)
	r.e.heartbeat()
	r.e.tick(ctx)
	if r.e.status()["state"] != "policy" {
		t.Fatal("a running executor must keep the episode")
	}
	r.exec.set("error")
	r.clock.advance(1100 * time.Millisecond)
	r.e.heartbeat()
	r.e.tick(ctx)
	if r.e.status()["state"] != "idle" {
		t.Fatal("state error must end the episode")
	}
}

func TestStopPreemptsBlockedTakeover(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	toPolicy(t, r)
	r.exec.block = make(chan struct{}) // executor stop hangs
	done := make(chan error, 1)
	go func() { done <- r.e.takeOver(ctx) }()
	time.Sleep(20 * time.Millisecond)
	if err := r.e.stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("take_over must return once stop cancels it")
	}
	if r.e.status()["state"] != "no_session" {
		t.Fatal("stop ends the session")
	}
	if err := r.e.stop(ctx); err != nil {
		t.Fatal("stop must be idempotent")
	}
}
```

Note for the last test: `abort` uses fresh contexts, so the hanging fake also blocks `abort`'s executor stop until its
own timeout. Give `abort`'s best-effort calls a short bound in tests by setting `r.e.timings.execStop` and
`r.e.timings.call` to 50 ms in that test before calling `stop`, or unblock with `close(r.exec.block)` right after
`stop` is called from a goroutine. Pick one and keep the test deterministic.

- [ ] **Step 2:** run → FAIL. **Step 3:** implement. **Step 4:** `go test ./... -race -v` → PASS (run with `-race`).
- [ ] **Step 5: Commit**: `git commit -am "Episode runner: heartbeat watchdog, run polling, stop pre-empts operations"`

---

### Task 6: The Viam resource and registration

**Files:**
- Create: `runner.go`
- Modify: `cmd/module/main.go`, `meta.json`
- Test: `runner_test.go` (config validation and `DoCommand` dispatch against an engine with fakes)

`runner.go`:

```go
package capturecontrolwebapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.viam.com/rdk/components/sensor"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/services/generic"
)

var RunnerModel = resource.NewModel("viam-labs", "capture-control", "episode-runner")

type RunnerConfig struct {
	Executor              string `json:"executor"`
	Teleop                string `json:"teleop,omitempty"`
	CaptureSensor         string `json:"capture_sensor"`
	HeartbeatTimeoutMs    int    `json:"heartbeat_timeout_ms,omitempty"`
	TakeoverHoldMs        int    `json:"takeover_hold_ms,omitempty"`
	CallTimeoutMs         int    `json:"call_timeout_ms,omitempty"`
	ExecutorStopTimeoutMs int    `json:"executor_stop_timeout_ms,omitempty"`
	PolicyStartTimeoutMs  int    `json:"policy_start_timeout_ms,omitempty"`
	TeleopSafeTakeover    bool   `json:"teleop_safe_takeover,omitempty"`
}

func (c *RunnerConfig) Validate(path string) ([]string, []string, error) {
	if c.Executor == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "executor")
	}
	if c.CaptureSensor == "" {
		return nil, nil, resource.NewConfigValidationFieldRequiredError(path, "capture_sensor")
	}
	var optional []string
	if c.Teleop != "" {
		optional = []string{c.Teleop}
	}
	return []string{c.Executor, c.CaptureSensor}, optional, nil
}

func (c *RunnerConfig) timings() timings {
	t := defaultTimings()
	ms := func(v int, d *time.Duration) {
		if v > 0 {
			*d = time.Duration(v) * time.Millisecond
		}
	}
	ms(c.HeartbeatTimeoutMs, &t.heartbeat)
	ms(c.TakeoverHoldMs, &t.hold)
	ms(c.CallTimeoutMs, &t.call)
	ms(c.ExecutorStopTimeoutMs, &t.execStop)
	ms(c.PolicyStartTimeoutMs, &t.policyStart)
	return t
}

func init() {
	resource.RegisterService(generic.API, RunnerModel,
		resource.Registration[resource.Resource, *RunnerConfig]{Constructor: newRunner})
}

type runner struct {
	resource.Named
	resource.AlwaysRebuild
	e      *engine
	cancel context.CancelFunc
	done   chan struct{}
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func moduleLogDir() string {
	base := os.Getenv("VIAM_MODULE_DATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "episode-runner")
}

func newRunner(ctx context.Context, deps resource.Dependencies, raw resource.Config, logger logging.Logger) (resource.Resource, error) {
	conf, err := resource.NativeConfig[*RunnerConfig](raw)
	if err != nil {
		return nil, err
	}
	exec, err := generic.FromProvider(deps, conf.Executor)
	if err != nil {
		return nil, fmt.Errorf("executor %q: %w", conf.Executor, err)
	}
	capture, err := sensor.FromProvider(deps, conf.CaptureSensor)
	if err != nil {
		return nil, fmt.Errorf("capture_sensor %q: %w", conf.CaptureSensor, err)
	}
	var teleop doer
	if conf.Teleop != "" {
		t, err := generic.FromProvider(deps, conf.Teleop)
		if err != nil {
			return nil, fmt.Errorf("teleop %q: %w", conf.Teleop, err)
		}
		teleop = t
	}
	e := newEngine(engineDeps{
		exec: exec, teleop: teleop, capture: capture,
		timings: conf.timings(), safeTakeover: conf.TeleopSafeTakeover,
		logDir: moduleLogDir(), now: time.Now, newID: newID,
		sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-time.After(d):
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	})
	mctx, cancel := context.WithCancel(context.Background())
	r := &runner{Named: raw.ResourceName().AsNamed(), e: e, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		tk := time.NewTicker(50 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-mctx.Done():
				return
			case <-tk.C:
				e.tick(mctx)
			}
		}
	}()
	return r, nil
}

func (r *runner) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, _ := cmd["command"].(string)
	ok := map[string]interface{}{"ok": true}
	switch name {
	case "status":
		return r.e.status(), nil
	case "heartbeat":
		r.e.heartbeat()
		return ok, nil
	case "start_session":
		task, _ := cmd["task"].(string)
		checkpoint, _ := cmd["checkpoint"].(string)
		fps, _ := cmd["fps"].(float64)
		return ok, r.e.startSession(task, checkpoint, fps)
	case "start_episode":
		return ok, r.e.startEpisode(ctx)
	case "take_over":
		return ok, r.e.takeOver(ctx)
	case "label":
		outcome, _ := cmd["outcome"].(string)
		return ok, r.e.label(ctx, outcome)
	case "stop":
		return ok, r.e.stop(ctx)
	case "log":
		session, _ := cmd["session"].(string)
		segs, err := r.e.log(session)
		if err != nil {
			return nil, err
		}
		out := make([]interface{}, len(segs))
		for i, s := range segs {
			out[i] = map[string]interface{}{
				"session": s.Session, "episode": s.Episode, "source": s.Source, "task": s.Task,
				"checkpoint": s.Checkpoint, "t_start": s.TStart.Format(time.RFC3339Nano),
				"t_end": s.TEnd.Format(time.RFC3339Nano), "outcome": s.Outcome, "error": s.Error,
			}
		}
		return map[string]interface{}{"segments": out}, nil
	}
	return nil, fmt.Errorf("unknown command %q", name)
}

func (r *runner) Close(ctx context.Context) error {
	err := r.e.stop(ctx)
	r.cancel()
	<-r.done
	return err
}
```

Add `func (e *engine) log(session string) ([]segment, error)` to the engine (current session when empty; empty list
with no session).

`cmd/module/main.go`: `module.ModularMain(resource.APIModel{API: generic.API, Model: capturecontrolwebapp.Model},
resource.APIModel{API: generic.API, Model: capturecontrolwebapp.RunnerModel})`.

`meta.json` `models`: add
`{"api": "rdk:service:generic", "model": "viam-labs:capture-control:episode-runner", "markdown_link": "README.md#episode-runner", "short_description": "Runs policy episodes with outcome labels and human takeover"}`.

- [ ] **Step 1: Failing tests** `runner_test.go`: `Validate` (missing executor → error; missing capture_sensor →
  error; teleop listed as optional only); `timings()` applies overrides and keeps defaults for zeros; `DoCommand` on a
  `runner{e: <engine from newRig>}`: `status` has `episode_runner`, unknown command errors, `start_session` then
  `status` shows the session, `log` returns `segments`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** `go test ./... -race` and `go build ./...` → PASS.
- [ ] **Step 5: Commit**: `git commit -am "Episode runner: Viam resource, config, registration"`

---

### Task 7: TypeScript helpers — status detection, outcome tags, summary

**Files:** Create `src/runner.ts`, `src/runner.test.ts`; modify `src/review.ts` (`parseTags` also returns `source`,
`outcome`, `checkpoint`, `episode`).

`src/runner.ts` (pure, no Svelte/SDK imports):

```ts
export type RunnerState = 'no_session' | 'idle' | 'starting' | 'policy' | 'correction';
export type Outcome = 'success' | 'failure' | 'takeover' | 'error' | 'stopped';
export type RunnerStatus = {
  episode_runner: true;
  state: RunnerState;
  session: { id: string; task: string; checkpoint: string; fps: number; episodes: number; counts: Record<string, number> } | null;
  episode: { id: number; source: 'policy' | 'correction'; started: number } | null;
  last_error: string;
};
export type Segment = {
  session: string; episode: number; source: 'policy' | 'correction'; task: string; checkpoint: string;
  t_start: string; t_end: string; outcome: Outcome; error: string;
};

export function asRunnerStatus(r: unknown): RunnerStatus | undefined {
  const o = r as Partial<RunnerStatus> | null;
  return o && o.episode_runner === true && typeof o.state === 'string' ? (o as RunnerStatus) : undefined;
}

/** The outcome for a synced sequence, matched by its session, episode and source tags; undefined if not in the log. */
export function outcomeFor(tags: string[], segs: Segment[]): Outcome | undefined {
  const get = (k: string) => tags.find((t) => t.startsWith(`${k}:`))?.slice(k.length + 1);
  const session = get('session'), episode = Number(get('episode')), source = get('source');
  return segs.find((s) => s.session === session && s.episode === episode && s.source === source)?.outcome;
}

export const withOutcome = (tags: string[], o: Outcome): string[] => [...tags.filter((t) => !t.startsWith('outcome:')), `outcome:${o}`];

export function summary(segs: Segment[]) {
  const policy = segs.filter((s) => s.source === 'policy');
  const n = (o: Outcome, src: Segment[] = policy) => src.filter((s) => s.outcome === o).length;
  const judged = n('success') + n('failure') + n('takeover');
  return {
    episodes: policy.length,
    success: n('success'), failure: n('failure'), takeover: n('takeover'), error: n('error'), stopped: n('stopped'),
    successRate: judged ? n('success') / judged : undefined,
    corrections: segs.filter((s) => s.source === 'correction').length,
  };
}
```

- [ ] **Step 1: Failing tests** `src/runner.test.ts` (`node:test` + `node:assert/strict`, style of `review.test.ts`):
  `asRunnerStatus` accepts a runner reply, rejects a vla:controller reply (`{state:'running', …}` without
  `episode_runner`) and a teleop reply (`{running:true}`); `outcomeFor` matches on all three tags and returns undefined
  for a different source or episode; `withOutcome` replaces an existing outcome tag; `summary` counts and computes the
  success rate over success + failure + takeover (errors and operator stops excluded). Extend `review.test.ts` for the
  new `parseTags` fields.
- [ ] **Step 2:** `npm test` → FAIL. **Step 3:** implement. **Step 4:** `npm test` and `npm run check` → PASS.
- [ ] **Step 5: Commit**: `git commit -am "Episode runner UI helpers: status detection, outcome tags, summary"`

---

### Task 8: Policy session UI

**Files:** Create `src/PolicySession.svelte`; modify `src/Recorder.svelte`.

Requirements (spec "Web app"):
- **Discovery** in `Recorder.svelte`: beside the teleop discovery, probe generic services with
  `{command:'status'}` and keep those where `asRunnerStatus` matches (`runners: string[]`). With exactly one runner,
  the start screen shows a **Policy session** button next to the existing recorder start; it opens `PolicySession`
  in place of the recorder view (same layout pattern as `Review`). Disabled while a demonstration session is active.
- **`PolicySession.svelte`** props: the robot client, the runner name, camera names, the saved task list and active
  task, and the home function/props already used by the recorder (H keeps the existing `robot.ts` `home()` path and is
  allowed only when the runner state is `idle` or `no_session`).
  - Setup form: task (saved tasks), checkpoint label (text, remembered in `localStorage` key `policyCheckpoint`), fps
    (default from recorder settings). **Start session** → `start_session`.
  - A status poll every 500 ms (`status`) drives the view; while the state is `starting`, `policy` or `correction`, also
    send `heartbeat` every 500 ms. Use `rb.withTimeout` for every call.
  - Live view: `CameraFeed` grid (reuse the recorder's), a state badge (`starting` shows "Loading policy…"), episode
    number, elapsed since `episode.started`, the session tally from `session.counts`, `last_error` when set.
  - Keys (ignored while typing in inputs, like the recorder): Space → `start_episode` (idle); S/F → `label`
    `success`/`failure` (policy or correction); T → `take_over` (policy); H → home (idle); Esc → `stop`; `?` → shortcut
    overlay.
  - **End session** → `stop`, then fetch `log` and show the summary from `summary()` (success rate as a percentage
    with the judged count, corrections count), with a **Review session** button that opens the existing Review for that
    session id.
  - On component destroy with an active episode: send `stop` (best effort).
- `npm run check` and `npm run build` must pass.

- [ ] **Step 1:** implement the discovery and the component. **Step 2:** `npm test`, `npm run check`, `npm run build`
  → pass. **Step 3: Commit**: `git commit -am "Policy session UI for the episode runner"`

---

### Task 9: Review filters, outcome tagging, README

**Files:** Modify `src/data.ts`, `src/Review.svelte`, `README.md`.

- `data.ts`: `setOutcome(seqId, outcome)` mirroring `setReview` (re-read tags, `withOutcome`, `updateSequence` with
  `paths: ['sequence_tags']`).
- `Review.svelte`:
  - Filters for `source` (all / policy / correction) and `outcome` (all / success / failure / takeover / error /
    stopped) next to the existing task/session/review filters, using the new `parseTags` fields.
  - When opened for a runner session (pass the runner name and session id from `PolicySession`), fetch the runner's
    `log` for that session and, as sequences sync, apply `setOutcome` to each sequence whose `outcomeFor` result differs
    from its current `outcome:` tag. Show the outcome beside the review state in the episode list.
- `README.md`: an **Episode runner** section (anchor `#episode-runner`): what it does; the config block from the spec;
  the dependency contracts table; `teleop_safe_takeover` and the pose check (record Task 1's result for the SO-101);
  the Policy session keys; the tags and the session log location; the safety behaviour.

- [ ] **Step 1:** implement. **Step 2:** `make test` (Go + TS) and `npm run check` → pass. **Step 3: Commit**:
  `git commit -am "Review: source and outcome filters, outcome tags from the runner log; README"`

---

### Task 10 ★ (controller + user): build and hardware smoke

1. `make module.tar.gz` locally; run the module on a machine with vla:controller, an SO-101 teleop and the
   capture-control sensor (local module path), plus the episode-runner config (`teleop_safe_takeover` per Task 1).
2. Policy session: three episodes labelled S/F, one takeover with an S-labelled correction, one heartbeat loss (close
   the tab mid-episode: the arm must stop within ~2 s).
3. After sync: sequences carry `source:` and `checkpoint:` tags, review applies `outcome:` tags, and the correction's
   first frame shows the takeover pose.
4. Report results to the user; pushing, PRs and a release are separate asks.
