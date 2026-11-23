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
    "stop_wait_ms": 5000
  }
}
```

`executor`, `teleop` and `capture_sensor` are required dependencies. `teleop` may be omitted, in which case
`take_over` is refused (policy episodes and labels still work).

### Dependency contracts

The runner uses only these DoCommands.

| Role | Command | Reply the runner reads | Today |
|---|---|---|---|
| Executor | `{"command":"start","task":"<text>"}` | — (error = refused) | `viam-labs:vla:controller` as is |
| | `{"command":"stop"}` | — | |
| | `{"command":"status"}` | `state` (`running` = moving the arm; anything else = not), `last_error` | |
| Teleop | `{"command":"start"}`, `{"command":"stop"}` | — | `devrel:so101:teleop` as is |
| | `{"command":"status"}` | `running` (bool), `last_error` | |
| Capture | `{"start_capture":true,"frequency_hz":<fps>,"tags":[…]}`, `{"stop_capture":true}` | — | `viam:capture-control:capture-control-sensor` |

**Teleop must start from the follower's current pose** (no jump to wherever the leader is). This is part of the
contract. Verify `devrel:so101:teleop` (`manage_leader_torque: true`) does this before building on it; if it does not,
the runner refuses `take_over` for that device and the gap is reported upstream.

### Runner API (DoCommand)

| Command | Arguments | Effect | Allowed in |
|---|---|---|---|
| `start_session` | `task`, `checkpoint` (label), `fps` | Opens a session, new `session` id | idle, no session |
| `start_episode` | — | Starts capture, then the executor | idle, session open |
| `take_over` | — | Policy segment ends (`outcome: takeover`); correction capture starts; teleop starts | policy |
| `label` | `outcome`: `success` \| `failure` | Ends the current policy run or correction with that outcome | policy, correction |
| `stop` | — | Stops executor, teleop and capture; ends the session; idempotent | any |
| `heartbeat` | — | Keeps a policy run or correction alive | any |
| `status` | — | `state`, `session` (task, checkpoint, fps, id, counts), `episode` (id, source, started), `last_error` | any |
| `log` | `session` (default: current) | The session's log entries | any |

Commands not allowed in the current state return an error naming the state; nothing changes.

## Episode state machine

```
idle ──start_episode──► policy ──label──► idle
                          │
                          └──take_over──► correction ──label──► idle
any ──stop / heartbeat lost / executor error / dependency timeout──► idle
```

- **start_episode**: `start_capture` with tags `task:<task>`, `session:<id>`, `episode:<n>`, `source:policy`,
  `checkpoint:<label>`, `fps:<fps>`; then `executor.start(task)`. If the executor refuses, capture is stopped and the
  episode is logged with `outcome: error`.
- **label (policy)**: `executor.stop`, wait until its status is not `running` (up to `stop_wait_ms`), `stop_capture`,
  log the outcome. The executor finishing on its own does not end the episode: the operator judges success.
- **take_over**, in this order:
  1. `executor.stop`, wait until not `running` (up to `stop_wait_ms`; on timeout: stop everything, `outcome: error`).
  2. `stop_capture`; log the policy segment with `outcome: takeover`.
  3. `start_capture` with tags `task:`, `session:`, the same `episode:<n>`, `source:correction`, `checkpoint:`, `fps:`.
  4. Hold `takeover_hold_ms` (capture's first frame lags `start_capture`; without the hold the correction opens after
     the human has already moved — the same lesson as in sim).
  5. `teleop.start`.
- **label (correction)**: `teleop.stop`, `stop_capture`, log the correction's outcome.
- Every dependency call is bounded by `call_timeout_ms`.

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

- **Heartbeat**: while the state is policy or correction, the web app sends `heartbeat` about every 500 ms. A gap of
  `heartbeat_timeout_ms` stops the executor, teleop and capture; the segment is logged `outcome: stopped`.
- **Executor error**: the runner polls executor `status` about once a second during a policy run; `last_error` set or
  a state other than `running` (while the runner expects running) ends the episode with `outcome: error`.
- **Dependency timeout**: any bounded call that times out stops everything.
- **Reconfigure or close**: stops everything first.
- All stop paths are idempotent; `status` always reports the state and the last error.

## Web app

- **Discovery**: a generic service whose `status` reply has a runner `state` field. If present, the start screen
  offers **Policy session** beside **Record demonstrations**.
- **`PolicySession.svelte`** (reuses `CameraFeed` and the home flow; talks only to the runner during a session):
  setup (task from the saved list, checkpoint label, fps); live view (state, episode, elapsed, session tally of
  successes, failures and takeovers); heartbeat while active.

  | Key | Action |
  |---|---|
  | Space | Start the next policy episode |
  | S / F | Success / failure for the current policy run or correction |
  | T | Take over (policy state only) |
  | H | Home (idle only) |
  | Esc | Stop |

- **Review**: Episodes and Review gain `source` and `outcome` filters; after a session, review applies outcome tags
  from the runner's log beside the existing keep/discard tags.
- **Session summary**: success rate for the checkpoint, interventions per episode, median time to success, from the
  log.

## Testing

- **Go**: state-machine tests with fake executor, teleop and capture: every transition and refusal; takeover order
  (executor stopped → capture switched → hold → teleop started); heartbeat loss, executor error and call timeout each
  stop everything; idempotent stop; log lines; reconfigure stops a running episode.
- **TypeScript**: session logic and log → tag mapping, in the style of `capture.test.ts` and `review.test.ts`.
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
