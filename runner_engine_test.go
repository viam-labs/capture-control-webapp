package capturecontrolwebapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.viam.com/rdk/logging"
)

// fakeDoer records commands and answers like the real dependency.
type fakeDoer struct {
	mu      sync.Mutex
	kind    string // "executor", "teleop", "capture"
	rec     []map[string]interface{}
	state   string // executor state
	running bool   // teleop running
	failOn  string // command name that returns an error
	block   chan struct{}
}

func cmdName(cmd map[string]interface{}) string {
	if c, ok := cmd["command"].(string); ok {
		return c
	}
	for _, k := range []string{"start_capture", "stop_capture"} {
		if _, ok := cmd[k]; ok {
			return k
		}
	}
	return ""
}

func (f *fakeDoer) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	f.mu.Lock()
	f.rec = append(f.rec, cmd)
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
	for _, c := range f.rec {
		if n := cmdName(c); n != "status" {
			out = append(out, n)
		}
	}
	return out
}

func (f *fakeDoer) calls() []map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]interface{}(nil), f.rec...)
}

func (f *fakeDoer) set(state string) { f.mu.Lock(); f.state = state; f.mu.Unlock() }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type rig struct {
	e                     *engine
	exec, teleop, capture *fakeDoer
	clock                 *fakeClock
	sleptMu               sync.Mutex
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
		sleep: func(ctx context.Context, d time.Duration) error {
			r.sleptMu.Lock()
			r.slept = append(r.slept, d)
			r.sleptMu.Unlock()
			return ctx.Err()
		},
		newID:  func() string { return "s1" },
		logger: logging.NewTestLogger(t),
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
	st := r.e.status()
	if st["state"] != "starting" || st["episode"].(map[string]interface{})["started"] != r.clock.now().UnixMilli() {
		t.Fatalf("status %v", st)
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
	c := r.capture.calls()[0]
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
	if len(segs) != 1 || segs[0].Outcome != "success" || segs[0].Source != "policy" || !segs[0].Captured {
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
	if len(segs) != 1 || segs[0].Outcome != "error" || segs[0].Captured || r.e.status()["state"] != "idle" {
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

func inPolicy(t *testing.T, r *rig) {
	t.Helper()
	startedSession(t, r)
	if err := r.e.startEpisode(context.Background()); err != nil {
		t.Fatal(err)
	}
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(context.Background())
	if r.e.status()["state"] != "policy" {
		t.Fatal("not in policy")
	}
}

func lastOutcome(t *testing.T, r *rig) string {
	t.Helper()
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) == 0 {
		t.Fatal("no segments")
	}
	return segs[len(segs)-1].Outcome
}

func TestCaptureStartFailureAborts(t *testing.T) {
	r := newRig(t, true)
	r.capture.failOn = "start_capture"
	startedSession(t, r)
	_ = r.e.startEpisode(context.Background())
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(context.Background())
	if r.e.status()["state"] != "idle" || lastOutcome(t, r) != "error" {
		t.Fatalf("status %v", r.e.status())
	}
	if got := r.exec.names(); got[len(got)-1] != "stop" {
		t.Fatalf("executor must be stopped: %v", got)
	}
}

func TestLabelRefusals(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	if err := r.e.label(ctx, "success"); err == nil {
		t.Fatal("label in idle must be refused")
	}
	r.e.startEpisode(ctx)
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(ctx)
	if err := r.e.label(ctx, "bogus"); err == nil {
		t.Fatal("bad outcome must be refused")
	}
	if r.e.status()["state"] != "policy" {
		t.Fatal("a bad outcome must not disturb the episode")
	}
}

func TestLabelExecutorStopFailureAborts(t *testing.T) {
	r := newRig(t, true)
	inPolicy(t, r)
	r.exec.failOn = "stop"
	if err := r.e.label(context.Background(), "success"); err == nil {
		t.Fatal("expected the stop failure")
	}
	if r.e.status()["state"] != "idle" || lastOutcome(t, r) != "error" {
		t.Fatalf("status %v", r.e.status())
	}
	if got := r.capture.names(); got[len(got)-1] != "stop_capture" {
		t.Fatalf("capture must be stopped: %v", got)
	}
}

func TestAbortTwiceIsNoop(t *testing.T) {
	r := newRig(t, true)
	inPolicy(t, r)
	r.e.abort("error", "boom")
	n := len(r.exec.names()) + len(r.capture.names())
	r.e.abort("error", "again")
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) != 1 || n != len(r.exec.names())+len(r.capture.names()) || r.e.status()["last_error"] != "boom" {
		t.Fatalf("second abort changed things: %v %v", segs, r.e.status())
	}
}

type statusFailDoer struct{ *fakeDoer }

func (s statusFailDoer) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	if cmdName(cmd) == "status" {
		return nil, errors.New("status down")
	}
	return s.fakeDoer.DoCommand(ctx, cmd)
}

func TestStatusFailingThreeTimesAborts(t *testing.T) {
	r := newRig(t, true)
	r.e.exec = statusFailDoer{r.exec}
	startedSession(t, r)
	_ = r.e.startEpisode(context.Background())
	for i := 0; i < 3; i++ {
		if r.e.status()["state"] != "starting" {
			t.Fatalf("aborted after %d failures", i)
		}
		r.clock.advance(250 * time.Millisecond)
		r.e.tick(context.Background())
	}
	if r.e.status()["state"] != "idle" || lastOutcome(t, r) != "error" {
		t.Fatalf("status %v", r.e.status())
	}
}

func TestStatusFailingPastStartTimeoutAborts(t *testing.T) {
	r := newRig(t, true)
	r.e.exec = statusFailDoer{r.exec}
	startedSession(t, r)
	_ = r.e.startEpisode(context.Background())
	r.clock.advance(121 * time.Second)
	r.e.tick(context.Background()) // first failure, but the timeout has passed
	if r.e.status()["state"] != "idle" {
		t.Fatalf("status %v", r.e.status())
	}
}

func TestTakeoverOrder(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	inPolicy(t, r)
	if err := r.e.takeOver(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.exec.names(); got[len(got)-1] != "stop" {
		t.Fatalf("executor %v", got)
	}
	if got := r.capture.names(); len(got) != 3 || got[1] != "stop_capture" || got[2] != "start_capture" {
		t.Fatalf("capture %v", got)
	}
	tags := r.capture.calls()[2]["tags"].([]string)
	if tags[2] != "episode:1" || tags[3] != "source:correction" {
		t.Fatalf("correction tags %v", tags)
	}
	r.sleptMu.Lock()
	slept := append([]time.Duration(nil), r.slept...)
	r.sleptMu.Unlock()
	if len(slept) == 0 || slept[len(slept)-1] != 500*time.Millisecond {
		t.Fatalf("hold %v", slept)
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
	if len(segs) != 2 || segs[0].Outcome != "takeover" || segs[1].Source != "correction" || segs[1].Outcome != "success" || !segs[0].Captured || !segs[1].Captured {
		t.Fatalf("log %v", segs)
	}
	if got := r.teleop.names(); got[len(got)-1] != "stop" {
		t.Fatalf("teleop %v", got)
	}
	if r.e.status()["state"] != "idle" {
		t.Fatal("label must end the correction")
	}
}

func refusedTakeover(t *testing.T, r *rig) {
	t.Helper()
	inPolicy(t, r)
	if err := r.e.takeOver(context.Background()); err == nil {
		t.Fatal("take_over must be refused")
	}
	if r.e.status()["state"] != "policy" || len(r.teleop.names()) != 0 {
		t.Fatal("a refused take_over must change nothing")
	}
}

func TestTakeoverRefusedWithoutSafeTeleop(t *testing.T) {
	refusedTakeover(t, newRig(t, false))
}

func TestTakeoverRefusedWithoutTeleop(t *testing.T) {
	r := newRig(t, true)
	r.e.teleop = nil
	refusedTakeover(t, r)
}

func TestTakeoverTeleopStartFailure(t *testing.T) {
	r := newRig(t, true)
	inPolicy(t, r)
	r.teleop.failOn = "start"
	if err := r.e.takeOver(context.Background()); err == nil {
		t.Fatal("expected the teleop failure")
	}
	if got := r.teleop.names(); got[len(got)-1] != "stop" {
		t.Fatalf("teleop %v", got)
	}
	if got := r.capture.names(); got[len(got)-1] != "stop_capture" {
		t.Fatalf("capture %v", got)
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if r.e.status()["state"] != "idle" || len(segs) != 2 || segs[0].Outcome != "takeover" ||
		segs[1].Source != "correction" || segs[1].Outcome != "error" {
		t.Fatalf("status %v log %v", r.e.status(), segs)
	}
	if len(r.exec.names()) != 2 { // start, stop: abort must not stop the executor again
		t.Fatalf("executor %v", r.exec.names())
	}
}

func TestTakeoverCaptureStartFailure(t *testing.T) {
	r := newRig(t, true)
	inPolicy(t, r)
	r.capture.failOn = "start_capture"
	if err := r.e.takeOver(context.Background()); err == nil {
		t.Fatal("expected the capture failure")
	}
	if got := r.capture.names(); got[len(got)-1] != "stop_capture" {
		t.Fatalf("capture %v", got)
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if r.e.status()["state"] != "idle" || len(segs) != 2 || segs[1].Outcome != "error" || segs[1].TStart.IsZero() {
		t.Fatalf("status %v log %v", r.e.status(), segs)
	}
}

func TestPolicyCaptureStartFailureStopsCapture(t *testing.T) {
	r := newRig(t, true)
	r.capture.failOn = "start_capture"
	startedSession(t, r)
	_ = r.e.startEpisode(context.Background())
	r.exec.set("running")
	r.clock.advance(250 * time.Millisecond)
	r.e.tick(context.Background())
	if got := r.capture.names(); got[len(got)-1] != "stop_capture" {
		t.Fatalf("capture %v", got)
	}
}

func TestHeartbeatLossStopsPolicy(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	inPolicy(t, r)
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
	inPolicy(t, r)
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

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func hasName(f *fakeDoer, name string) func() bool {
	return func() bool {
		for _, n := range f.names() {
			if n == name {
				return true
			}
		}
		return false
	}
}

func TestStopPreemptsBlockedTakeover(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	inPolicy(t, r)
	r.e.timings.execStop = 2 * time.Second
	r.exec.block = make(chan struct{}) // executor stop hangs; its RPC is not cancelled by stop
	done := make(chan error, 1)
	go func() { done <- r.e.takeOver(ctx) }()
	waitFor(t, "take_over's executor stop", hasName(r.exec, "stop"))
	stopped := make(chan error, 1)
	start := time.Now()
	go func() { stopped <- r.e.stop(ctx) }()
	waitFor(t, "stop to cancel the operation", func() bool {
		r.e.mu.Lock()
		defer r.e.mu.Unlock()
		return r.e.stopping > 0
	})
	close(r.exec.block) // the stop RPC completes; take_over must notice the cancellation, not carry on
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("take_over must return once stop cancels it, well inside execStop")
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatal("stop must pre-empt, not wait out execStop")
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) == 0 || segs[0].Outcome != "stopped" {
		t.Fatalf("a pre-empted take_over must log the policy segment as stopped, got %v", segs)
	}
	if r.e.status()["state"] != "no_session" {
		t.Fatal("stop ends the session")
	}
	if err := r.e.stop(ctx); err != nil {
		t.Fatal("stop must be idempotent")
	}
}

func TestStopDuringBlockedExecutorStart(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	startedSession(t, r)
	r.exec.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.e.startEpisode(ctx) }()
	waitFor(t, "executor start", hasName(r.exec, "start"))
	stopped := make(chan error, 1)
	go func() { stopped <- r.e.stop(ctx) }()
	waitFor(t, "abort's executor stop", hasName(r.exec, "stop"))
	close(r.exec.block)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("a cancelled start_episode must fail")
	}
	if r.e.status()["state"] != "no_session" {
		t.Fatal("stop ends the session")
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if len(segs) != 1 || segs[0].Outcome != "stopped" {
		t.Fatalf("log %v", segs)
	}
}

func TestHeartbeatsKeepSlowTakeoverAlive(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	inPolicy(t, r)
	r.exec.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- r.e.takeOver(ctx) }()
	for i := 0; i < 5; i++ { // 5 s of operator heartbeats while take_over is blocked
		time.Sleep(5 * time.Millisecond)
		r.clock.advance(time.Second)
		r.e.heartbeat()
		r.e.tick(ctx) // must neither abort nor block forever: it waits for opMu only to act
	}
	close(r.exec.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if r.e.status()["state"] != "correction" {
		t.Fatal("heartbeats during a slow takeover must keep it alive")
	}
}

func TestCallTimeoutStopsEverything(t *testing.T) {
	r := newRig(t, true)
	ctx := context.Background()
	inPolicy(t, r)
	r.e.timings.call = 30 * time.Millisecond
	r.e.timings.execStop = 30 * time.Millisecond
	r.capture.block = make(chan struct{}) // stop_capture hangs past call_timeout
	if err := r.e.label(ctx, "success"); err == nil {
		t.Fatal("a timed-out dependency call must fail the label")
	}
	close(r.capture.block)
	if r.e.status()["state"] != "idle" {
		t.Fatal("a call timeout must stop everything")
	}
	segs, _ := readSegments(r.e.logDir, "s1")
	if segs[len(segs)-1].Outcome != "error" {
		t.Fatalf("log %v", segs)
	}
}
