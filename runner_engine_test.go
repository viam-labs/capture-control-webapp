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
