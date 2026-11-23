package capturecontrolwebapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	StatusTimeoutMs       int    `json:"status_timeout_ms,omitempty"`
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
	ms(c.StatusTimeoutMs, &t.status)
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

func newID() string { // names the log file: time prefix + random suffix so sessions never collide
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
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
	var teleop doer // must stay a nil interface when unset: a typed nil would pass the take_over refusal check
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
		logDir: moduleLogDir(), now: time.Now, newID: newID, logger: logger,
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

func okOrErr(err error) (map[string]interface{}, error) {
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{"ok": true}, nil
}

func (r *runner) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	name, _ := cmd["command"].(string)
	switch name {
	case "status":
		return r.e.status(), nil
	case "heartbeat":
		r.e.heartbeat()
		return map[string]interface{}{"ok": true}, nil
	case "start_session":
		task, _ := cmd["task"].(string)
		checkpoint, _ := cmd["checkpoint"].(string)
		fps, _ := cmd["fps"].(float64) // JSON numbers arrive as float64
		return okOrErr(r.e.startSession(task, checkpoint, fps))
	case "start_episode":
		return okOrErr(r.e.startEpisode(ctx))
	case "take_over":
		return okOrErr(r.e.takeOver(ctx))
	case "label":
		outcome, _ := cmd["outcome"].(string)
		return okOrErr(r.e.label(ctx, outcome))
	case "stop":
		return okOrErr(r.e.stop(ctx))
	case "log":
		session, _ := cmd["session"].(string)
		if session != "" && (filepath.Base(session) != session || session == "." || session == "..") {
			return nil, fmt.Errorf("invalid session %q", session)
		}
		segs, err := r.e.log(session)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(segs) // segment's json tags are the wire format
		if err != nil {
			return nil, err
		}
		var out []interface{}
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
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
