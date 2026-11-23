package capturecontrolwebapp

import (
	"context"
	"testing"
	"time"
)

func TestRunnerValidate(t *testing.T) {
	if _, _, err := (&RunnerConfig{CaptureSensor: "c"}).Validate("p"); err == nil {
		t.Fatal("missing executor must fail")
	}
	if _, _, err := (&RunnerConfig{Executor: "e"}).Validate("p"); err == nil {
		t.Fatal("missing capture_sensor must fail")
	}
	req, opt, err := (&RunnerConfig{Executor: "e", CaptureSensor: "c", Teleop: "t"}).Validate("p")
	if err != nil || len(req) != 2 || req[0] != "e" || req[1] != "c" || len(opt) != 1 || opt[0] != "t" {
		t.Fatalf("req %v opt %v err %v", req, opt, err)
	}
	if _, opt, _ = (&RunnerConfig{Executor: "e", CaptureSensor: "c"}).Validate("p"); len(opt) != 0 {
		t.Fatalf("opt %v", opt)
	}
}

func TestRunnerTimings(t *testing.T) {
	if got := (&RunnerConfig{}).timings(); got != defaultTimings() {
		t.Fatalf("zero config must keep defaults: %v", got)
	}
	got := (&RunnerConfig{HeartbeatTimeoutMs: 1, TakeoverHoldMs: 2, CallTimeoutMs: 3,
		ExecutorStopTimeoutMs: 4, PolicyStartTimeoutMs: 5, StatusTimeoutMs: 6}).timings()
	d := defaultTimings()
	d.heartbeat, d.hold, d.call = time.Millisecond, 2*time.Millisecond, 3*time.Millisecond
	d.execStop, d.policyStart, d.status = 4*time.Millisecond, 5*time.Millisecond, 6*time.Millisecond
	if got != d {
		t.Fatalf("got %v want %v", got, d)
	}
}

func TestRunnerDoCommand(t *testing.T) {
	rig := newRig(t, true)
	r := &runner{e: rig.e}
	ctx := context.Background()
	do := func(cmd map[string]interface{}) (map[string]interface{}, error) { return r.DoCommand(ctx, cmd) }

	if st, err := do(map[string]interface{}{"command": "status"}); err != nil || st["episode_runner"] != true {
		t.Fatalf("status %v %v", st, err)
	}
	if _, err := do(map[string]interface{}{"command": "nope"}); err == nil {
		t.Fatal("unknown command must error")
	}
	if out, err := do(map[string]interface{}{"command": "log"}); err != nil || len(out["segments"].([]interface{})) != 0 {
		t.Fatalf("log %v %v", out, err)
	}
	if _, err := do(map[string]interface{}{"command": "start_session", "task": "pick", "checkpoint": "c1", "fps": float64(15)}); err != nil {
		t.Fatal(err)
	}
	st, _ := do(map[string]interface{}{"command": "status"})
	if s := st["session"].(map[string]interface{}); s["task"] != "pick" || s["fps"] != 15.0 {
		t.Fatalf("session %v", s)
	}
	if out, err := do(map[string]interface{}{"command": "log"}); err != nil || len(out["segments"].([]interface{})) != 0 {
		t.Fatalf("log %v %v", out, err)
	}
}

func TestRunnerLogRejectsTraversal(t *testing.T) {
	r := &runner{e: newRig(t, true).e}
	for _, s := range []string{"..", ".", "../x", "a/b", `a\b/c`} {
		if _, err := r.DoCommand(context.Background(), map[string]interface{}{"command": "log", "session": s}); err == nil {
			t.Fatalf("session %q must be rejected", s)
		}
	}
}

func TestRunnerErrorReplyHasNoOK(t *testing.T) {
	r := &runner{e: newRig(t, true).e}
	out, err := r.DoCommand(context.Background(), map[string]interface{}{"command": "start_episode"})
	if err == nil || out != nil {
		t.Fatalf("out %v err %v", out, err)
	}
}

func TestRunnerCloseStopsEpisode(t *testing.T) {
	rig := newRig(t, true)
	ctx := context.Background()
	startedSession(t, rig)
	if err := rig.e.startEpisode(ctx); err != nil {
		t.Fatal(err)
	}
	rig.exec.set("running")
	rig.clock.advance(250 * time.Millisecond)
	rig.e.tick(ctx)
	if rig.e.status()["state"] != "policy" {
		t.Fatal("not in policy")
	}
	done := make(chan struct{})
	close(done)
	r := &runner{e: rig.e, cancel: func() {}, done: done}
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	segs, err := readSegments(rig.e.logDir, "s1")
	if err != nil || len(segs) != 1 || segs[0].Outcome != "stopped" {
		t.Fatalf("segs %v %v", segs, err)
	}
	if got := rig.capture.names(); got[len(got)-1] != "stop_capture" {
		t.Fatalf("capture %v", got)
	}
	if got := rig.exec.names(); got[len(got)-1] != "stop" {
		t.Fatalf("exec %v", got)
	}
}
