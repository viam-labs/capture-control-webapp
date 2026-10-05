# Robot Demo Capture Controller — v1 Design

A LeLab-style web app (Viam single-machine app) for recording robot demonstration episodes labeled with a task, on top of Viam data capture.

## Goals

- Run a recording session: task label, episode count, episode duration, reset duration, fps.
- Each episode becomes one Viam **sequence** with tags, with data captured only while recording.
- Show live camera previews so the operator sees what's being recorded.

## Non-goals (v1)

Teleop (handled externally on the machine), episode review/playback, discard/re-record, LeRobot export, training.

## Architecture

```
Browser (dist/)  ──WebRTC DoCommand──▶  capture-control-sensor  ◀──10 Hz poll──  data manager
                                                                                  │ collectors (arm, cams…)
                                                                                  └─ sync → Viam Data + Sequences
```

- **No backend changes.** The existing Go module (`module.go`) keeps serving `dist/` via `vmodutils`.
- Capture is driven by [`viam:capture-control:capture-control-sensor`](https://github.com/viam-labs/capture-control-sensor), configured by the user on the machine:
  - sensor attributes: `resources: [{resource_name, method}, …]` (arm joints, cameras, etc.)
  - data manager attributes: `"capture_control_sensor": {"name": "<sensor>", "key": "overrides"}`
- Verified RDK behavior (`services/datamanager/builtin`):
  - The data manager polls the sensor at 10 Hz (`capturePollFrequency`), so episode boundaries are within ~100 ms.
  - `overrides` sets the per-resource `capture_frequency_hz` and `tags` live, without a reconfigure; a frequency of 0 closes the collector.
  - When an entry appears in `sequences`, a `.progseq` is written; when it disappears, it becomes a `.seq`, which sync uploads via `CreateSequence` (start/end time, resources, `sequence_tags`).
  - An orphaned `.progseq` (from a viam-server crash) is moved to `failed/`.

## Controller protocol (from the app)

| Moment | DoCommand |
|---|---|
| Episode start | `{"start_capture": true, "frequency_hz": <fps>, "tags": ["task:<label>", "session:<id>", "fps:<fps>"]}` |
| Episode end / session stop | `{"stop_capture": true}` |

- `start_capture` applies the tags to both the captured data and the sequence.
- Always send `frequency_hz`: the sensor's `default_capture_frequency_hz` defaults to 0, which would mean no capture.
- A single frequency applies to all controlled resources; this is a limitation of the sensor.
- `session:<id>` is a short random id generated when the session starts.

## Frontend

- **Stack:** Svelte 5 + Vite + `@viamrobotics/svelte-sdk`. Builds to `dist/index.html` with relative asset paths (`base: './'`). Connects with the existing `auth.js` (`getHostAndCredentials`).
- **Controller discovery:** list the machine's sensors and keep those whose `Readings()` contain both `overrides` and `sequences`. Auto-select if there's exactly one; otherwise show a dropdown. Show an empty state with setup instructions if there are none.
- **Layout (option B):**
  - Left column: the setup form (task, episodes, episode s, reset s, fps), which collapses once the session starts. Below it, a status panel showing the state (RECORDING / RESET / DONE), a countdown, `Episode k / N`, and the task.
  - Right: a grid of live camera previews. The cameras are the `resource_name`s from the controller's `overrides` reading that are camera resources on the machine.
- **Keys:** Space ends the current phase early. Esc stops the session.
- **Persistence:** the last-used form values are kept in `localStorage`, wrapped in try/catch.

## Session engine

A pure TS module, with no Svelte or SDK imports.

- States: `idle → recording ⇄ reset → done`.
- Inputs: `start(settings, now)`, `tick(now)`, `next(now)`, `stop()`.
- Outputs: the commands to send (`start_capture` / `stop_capture`) and the current view state (phase, remaining ms, episode index).
- The final episode goes straight from `recording` to `done`, with no reset.
- The UI drives `tick` from a `requestAnimationFrame` or interval loop and sends the emitted commands in order. It awaits each command before applying the next transition.

## Failure handling

- **`start_capture` fails:** stop the session, show the error, and attempt `stop_capture`.
- **`stop_capture` fails:** retry. If it still fails, show the "capture still running" banner. Never start the next episode while a sequence might be open.
- **Orphaned capture:** the sensor has no watchdog, so a closed tab would leave capture running.
  - On load, if the controller's `sequences` reading is non-empty, show a banner saying "capture still running" with a **Stop** button.
  - On `pagehide`, make a best-effort `stop_capture`.
- **Connection lost mid-session:** stop the session (`stop()`), and show the error. On reconnect, the orphan check above applies.

## Testing

- One `node:test` file for the session engine with a fake clock. It covers: the full N-episode run, skipping the final reset, `next()` during recording and during reset, `stop()` emitting `stop_capture` while recording, and `stop()` emitting nothing during reset (capture is already stopped).
- Manual end-to-end check on a real machine: run a 2-episode session, then confirm two sequences with the expected tags appear in Viam Data.

## Later

- An optional `max_duration_s` on `start_capture` in capture-control-sensor, so the robot closes stale sequences itself.
- An episode browser using `getSequence` / `getSequenceBinaryData`.
- Discarding an episode (`deleteSequence` or a `bad` tag).
- LeRobot export.
- Per-resource frequencies.
