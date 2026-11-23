# capture-control-webapp — Episode Runner Design (policy episodes and human takeover)

The episode runner lets an operator run a learned policy on a physical robot, mark each run a success or a failure,
and take over with teleop when the policy goes wrong, capturing the correction. Every rollout and every correction
lands in Viam data as a tagged sequence. This is the physical-robot version of the expert-gated DAgger loop proven in
sim (diff-ik `docs/results/2026-11-policy-dagger.md`: SmolVLA 23/30 → 27/30 from 49 corrections), with a human in
place of the scripted expert.

## Goals

- Run policy episodes from the web app and label each one success or failure, so every rollout is captured with an
  outcome (data for training that learns from all rollouts, and a success rate per checkpoint).
- Take over mid-episode: stop the policy, start teleop from the current pose, capture only the correction, label it.
- Work with any policy executor and any teleop device that implement a three-command contract.
- Stop the robot when the operator's browser goes away or a dependency fails.

## Non-goals (v1)

- Automatic triggers and a learned failure monitor (they will call `take_over` later).
- A diff-ik executor model (a follow-up in diff-ik: port `clients/python/policy_run.py`'s loop to the executor
  contract).
- A teleop queue across several machines.
- Training recipes that use the outcome labels.

## Architecture

A second model in this module: **`viam-labs:capture-control:episode-runner`**, a generic service in Go beside the
existing `viam-labs:capture-control:webapp`. The runner owns the session state machine and all calls to the robot
during a policy session; the web app is its operator UI.

```json
{
  "name": "episode-runner",
  "api": "rdk:service:generic",
  "model": "viam-labs:capture-control:episode-runner",
  "attributes": {
    "executor": "vla-controller",
    "teleop": "so101-teleop",
    "capture_sensor": "demo-capture",
    "heartbeat_timeout_ms": 2000,
    "takeover_hold_ms": 500,
    "call_timeout_ms": 5000,
    "status_timeout_ms": 1000,
    "executor_stop_timeout_ms": 15000,
    "policy_start_timeout_ms": 120000,
    "teleop_safe_takeover": true
  }
}
```

`executor` and `capture_sensor` are required dependencies; `teleop` is optional. Without it `take_over` is refused,
and policy episodes and labels still work.

The runner lives in the root package beside `module.go` (`runner.go`, its own `Config` and model name).
`cmd/module/main.go` passes a second `resource.APIModel{API: generic.API, Model: RunnerModel}` to
`module.ModularMain`, and `meta.json` `models` gains the matching entry.

### Dependency contracts

The runner uses only these DoCommands.

| Role | Command | Reply the runner reads | Today |
|---|---|---|---|
| Executor | `{"command":"start","task":"<text>"}` | — (error = refused) | `viam-labs:vla:controller` as is |
| | `{"command":"stop"}` | — | |
| | `{"command":"status"}` | `state`, `last_error` (see executor states below) | |
| Teleop | `{"command":"start"}`, `{"command":"stop"}` | — | `devrel:so101:teleop` as is |
| | `{"command":"status"}` | `running` (bool), `last_error` | |
| Capture | `{"start_capture":true,"frequency_hz":<fps>,"tags":[…]}`, `{"stop_capture":true}` | — | `viam:capture-control:capture-control-sensor` |

**Executor states.** The runner reads `state` from the executor's `status`, using vla:controller's values:

| `state` | Meaning for the runner |
|---|---|
| `waiting_for_policy` | Starting: the policy is loading or warming up. Not an error. vla:controller acks `start` at once and can stay here for minutes on a cold policy. |
| `running` | Moving the arm. |
| `error` | Terminal: the run failed. `last_error` explains it. |
| `stopped`, `idle` | Not running. Unexpected during a policy run: treated like `error`. |

`last_error` alone is not a failure: vla:controller sets it while staying `running` when it rides through a skipped
tick (tolerated read timeouts, refused delta-EE moves). Only `state` decides.

**Teleop must start from the follower's current pose** (no jump to wherever the leader is). This is part of the
contract, and it gates the whole takeover feature, so it is the plan's first task: test it on an SO-101 before any
runner code. The existing app hints it may not hold today: `robot.ts` / `Recorder.svelte` note that restarting teleop
drops leader torque and "the follower would copy the fall". If the check fails, the runner refuses `take_over` for
that device (config `teleop_safe_takeover: false`) and the gap goes upstream to the teleop module.

### Runner API (DoCommand)

| Command | Arguments | Effect | Allowed in |
|---|---|---|---|
| `start_session` | `task`, `checkpoint` (label), `fps` | Opens a session, new `session` id | idle, no session |
| `start_episode` | — | Starts the executor; capture starts once it reports `running` | idle, session open |
| `take_over` | — | Policy segment ends (`outcome: takeover`); correction capture starts; teleop starts | policy |
| `label` | `outcome`: `success` \| `failure` | Ends the current policy run or correction with that outcome | policy, correction |
| `stop` | — | Stops executor, teleop and capture; ends the session; idempotent | any |
| `heartbeat` | — | Keeps a policy run or correction alive | any |
| `status` | — | `episode_runner: true`, `state`, `session` (task, checkpoint, fps, id, counts; `null` when no session), `episode` (id, source, started; `null` when idle), `last_error` | any |
| `log` | `session` (default: current) | The session's log entries | any |

Commands not allowed in the current state return an error naming the state; nothing changes.

**Concurrency.** `start_episode` returns as soon as the executor acks `start`; a background poller then moves `starting` → `policy` (or ends the episode), so no DoCommand blocks through a policy load. `start_session`, `start_episode`, `take_over` and `label` are serialized by one operation lock and can
block for several seconds (an executor stop, a capture switch, the takeover hold). `heartbeat` and `status` never take
that lock, so heartbeats keep arriving during a takeover and the watchdog does not fire mid-transition. `stop` never
waits for the lock either: it cancels the in-flight operation's context, then runs the stop-everything path. A
cancelled operation leaves the runner in idle with the segment logged `outcome: stopped`.

## Episode state machine

```
no session ──start_session──► idle ──start_episode──► starting ──executor running──► policy ──label──► idle
                                                           │                              │
                                                           └──timeout / error──► idle     └──take_over──► correction ──label──► idle
any ──stop / heartbeat lost / executor error / dependency timeout──► idle (stop ends the session: no session)
```

- **start_episode**: `executor.start(task)`, then state `starting` while the executor reports `waiting_for_policy`.
  When it first reports `running`, `start_capture` with tags `task:<task>`, `session:<id>`, `episode:<n>`,
  `source:policy`, `checkpoint:<label>`, `fps:<fps>`, and state `policy`. Starting capture only then keeps a cold
  policy load out of the sequence. If the executor refuses, reports `error`, or is still not `running` after
  `policy_start_timeout_ms`, the runner stops it and logs `outcome: error` (no capture was started).
- **label (policy)**: `executor.stop` (bounded by `executor_stop_timeout_ms`), wait until its status is not `running`
  (same bound), `stop_capture`, log the outcome. The executor finishing on its own does not end the episode: the operator judges success.
- **take_over**, in this order:
  1. `executor.stop`, wait until not `running` (up to `executor_stop_timeout_ms`; on timeout: stop everything,
     `outcome: error`).
  2. `stop_capture`; log the policy segment with `outcome: takeover`.
  3. `start_capture` with tags `task:`, `session:`, the same `episode:<n>`, `source:correction`, `checkpoint:`, `fps:`.
  4. Hold `takeover_hold_ms` (capture's first frame lags `start_capture`; without the hold the correction opens after
     the human has already moved — the same lesson as in sim).
  5. `teleop.start`.
- **label (correction)**: `teleop.stop`, `stop_capture`, log the correction's outcome.
- Every dependency call is bounded by `call_timeout_ms`, except `executor.stop` and the wait after it, which use
  `executor_stop_timeout_ms`. vla:controller's stop cancels its loop, closes its stream (up to 5 s) and stops the arm,
  so it can exceed 5 s. An executor `stop` is never cut short: it is sent on a context that operator `stop` and
  operation cancellation cannot cancel (cancelling a DoCommand cancels the module's handler mid-stop, skipping
  `arm.stop()`); cancellation may interrupt only the status wait that follows it. The runner enters `starting` before
  sending `executor.start`, so a cancelled or timed-out start is always followed by an executor `stop`.

## Labels and the session log

Capture tags are fixed when capture starts, so outcomes cannot be capture tags. The runner appends one JSON line per
segment to `$VIAM_MODULE_DATA/episode-runner/<session>.jsonl`:

```json
{"session":"…","episode":3,"source":"policy","task":"…","checkpoint":"pi05-v1","t_start":"<RFC3339>","t_end":"<RFC3339>","outcome":"takeover","error":""}
```

`outcome` ∈ `success`, `failure`, `takeover`, `error`, `stopped`. The log is the source of truth. The web app's review
applies `outcome:<outcome>` to each synced sequence, matched by its `session:`, `episode:` and `source:` tags, using
the review code path that already tags sequences with the machine's credentials.

## Safety

- **Heartbeat**: while the state is starting, policy or correction (a tab closed during a cold policy load must not leave the arm to start moving unattended), the web app sends `heartbeat` about every 500 ms. A gap of
  `heartbeat_timeout_ms` stops the executor, teleop and capture; the segment is logged `outcome: stopped`.
- **Executor error**: the runner polls executor `status` every 200 ms in `starting` and about once a second in `policy`. Capture starts on the first `running` poll, so a policy sequence can miss up to about 200 ms of the run's first motion; that is expected. `state` of
  `error`, or `stopped` / `idle` while it expects a run, ends the episode with `outcome: error`. `last_error` with
  `state: running` is logged, not acted on.
- **Dependency timeout**: any bounded call that times out stops everything, except the executor `status` poll: each read is bounded by `status_timeout_ms` (default 1 s), and 3 consecutive failed reads stop everything (one dropped read on a busy network should not end an episode). The policy-start timeout applies whether or not the reads succeed.
- **Reconfigure or close**: stops everything first.
- All stop paths are idempotent; `status` always reports the state and the last error.

## Web app

- **Discovery**: a generic service whose `status` reply has `episode_runner: true`. (vla:controller's `status` also
  has a top-level `state`, so `state` alone would match the executor; the runner's reply has no boolean `running`, so
  the existing teleop discovery does not match it either.) If present, the start screen offers **Policy session**
  beside **Record demonstrations**.
- **`PolicySession.svelte`** (reuses `CameraFeed` and the home flow; during a policy run or correction it talks only
  to the runner):
  setup (task from the saved list, checkpoint label, fps); live view (state, episode, elapsed, session tally of
  successes, failures and takeovers); heartbeat while starting, policy or correction.

  | Key | Action |
  |---|---|
  | Space | Start the next policy episode |
  | S / F | Success / failure for the current policy run or correction |
  | T | Take over (policy state only) |
  | H | Home (runner idle only): the existing web-app path (`robot.ts` `home()`: teleop stop + arm-position-saver switches); the runner has no home command |
  | Esc | Stop |

- **Review**: Episodes and Review gain `source` and `outcome` filters; after a session, review applies outcome tags
  from the runner's log beside the existing keep/discard tags.
- **Session summary**: success rate for the checkpoint from the log. Interventions per episode and median time to
  success are optional for v1.

## Testing

- **Go**: state-machine tests with fake executor, teleop and capture: every transition and refusal; `starting` holds
  through `waiting_for_policy` and starts capture only on `running`; start timeout; `last_error` with `running` does
  not end a run, `state: error` does; takeover order (executor stopped → capture switched → hold → teleop started);
  heartbeats during a slow takeover keep it alive; heartbeat loss during `starting` stops the executor; `start_episode` returns before the executor reaches `running`; `stop` pre-empts a blocked `take_over`; heartbeat loss, executor
  error and call timeout each stop everything; idempotent stop; log lines; reconfigure stops a running episode.
- **TypeScript**: session logic and log → tag mapping, in the style of `capture.test.ts` and `review.test.ts`.
- **Teleop pose check (first task)**: on an SO-101, move the follower away from the leader with teleop off, start
  teleop, and confirm the follower holds its pose. Decides `teleop_safe_takeover` for that device.
- **Hardware smoke (SO-101)**: a few policy episodes labelled S/F, one takeover, one forced heartbeat loss (close the
  tab); then check synced sequences, tags, and that the correction's first frame shows the takeover pose.

## Decisions

| Decision | Choice | Why |
|---|---|---|
| Where the loop runs | A Go service on the machine, the web app as UI | Safety and takeover timing stay on the machine; scripts and later triggers can drive the same service |
| Module | A second model in this module | Shares the capture and review UI; one install for operators |
| Executor and teleop | Pluggable through a three-command contract | vla:controller and the SO-101 teleop already fit; diff-ik and other devices can follow |
| Outcome labels | Session log on the machine, applied as tags after sync | Capture tags are fixed at start; the log survives sync lag |
| Who decides success | The operator | The policy cannot tell success from a stall |
