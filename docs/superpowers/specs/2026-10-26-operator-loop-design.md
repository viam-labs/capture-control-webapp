# capture-control-webapp v2 — Operator Loop Design (spec A)

v2 makes the operator loop faster. It adds four features to the v0.1.0 recorder:

- Saved tasks you can swap between episodes.
- A teleop toggle.
- Home-pose resets.
- Richer keyboard controls.

The rest of the v2 backlog is out of scope:

- **Spec B:** episode review.
- **Spec C:** session notes, which are deferred.

Wireframe: https://claude.ai/artifact/6ssrz5AFbcnER8fmgwde25

## Goals

- Record demos for several subtasks in one session. Between episodes, the operator picks the task that the next episode records.
- Toggle the `devrel:so101:teleop` service from the app. Pressing T while recording works as an emergency stop.
- Send the leader and follower arms to a saved home pose on demand, using `erh:vmodutils:arm-position-saver` switches.
- Drive the whole loop from the keyboard.

## Non-goals

- Discarding or retagging an episode. This includes an episode ended by an emergency T, and belongs to spec B.
- Sharing tasks across devices. Tasks live in `localStorage`.
- Per-task episode targets, or home poses that depend on the task.
- Homing automatically on every reset. Home runs only when the operator presses it.

## Decisions

| Topic | Decision |
|---|---|
| Subtask model | Swap palette. A session mixes tasks, and each episode is tagged with the task that was active when it started. |
| Episode count | "Episodes N" stays the session total. The task chips show a count for each task. |
| Task storage | `localStorage`, edited inline in the app. |
| Home | Stop teleop, then move every saver in the home set in parallel, then restart teleop if it was running. This homes both arms, so the follower doesn't chase the leader away from home. |
| Home trigger | The H key or the Home button only. Not allowed while recording. |
| Home set | Picked in the app, using checkboxes over the discovered savers. |
| T while recording | Allowed as an emergency stop. It stops teleop, then ends the episode. |

## Session engine (`src/capture.ts`, pure)

- `Settings` loses `task`.
- `Session` gains three fields:
  - `task`: the selected task. The next `start_capture` uses it.
  - `episodeTask`: the tag of the current or last episode.
  - `counts: Record<string, number>`.
- `start(settings, task, id, now)` replaces `start(settings, id, now)`.
- `setTask(s, task)` sets `s.task`. Queuing needs no extra state:
  - The open capture is already tagged with `episodeTask`.
  - The UI shows a "next" chip when `phase === 'recording' && task !== episodeTask`.
- `startCapture` tags with `task:<s.task>` and sets `episodeTask = s.task`.
- Recording that ends through `next` or `stop` runs `counts[episodeTask]++`, because a sequence was written either way. A `stop` during reset leaves `counts` unchanged.

## Robot helpers (`src/robot.ts`, new)

These helpers use only the SDK clients, with no Svelte.

- `findTeleops(client)` returns the generic services whose `doCommand({command: 'status'})` result has a boolean `running`.
- `findSavers(client)` returns the switches whose `getNumberOfPositions()` labels are `['idle', 'update config', 'go to']`.
- `home(teleop, savers)`:
  1. Read the teleop status. If it's running, send `{command: 'stop'}`. Stopping teleop restores the leader's torque.
  2. Run `setPosition(2)` on every saver with `Promise.all`.
  3. If teleop was running, send `{command: 'start'}`. Both arms are now aligned, so the follower goes straight to streaming.
  4. If any move fails, don't restart teleop. Throw an error that names the saver.

## UI (`src/Recorder.svelte`)

Layout is unchanged: camera grid on top, drawer at the bottom.

**Idle drawer**

- A task palette replaces the Task input. It shows chips numbered 1–9 and an "add task" input, with rename and delete inline.
- Below the palette: the teleop pill and the Home button.
- Then the existing form: controller, episodes, episode length, reset, fps.
- A collapsed **Robot setup** section holds:
  - the teleop service select, auto-selected when there's only one;
  - home-set checkboxes, one for each discovered saver.

**Active drawer**

- The status line.
- The palette. Each chip shows its count, and a queued chip is drawn with a dashed border and a "next" tag.
- The teleop pill and the Home button. Home is disabled while recording.
- A hint line listing the keys.

**Teleop pill**

- It polls `{command: 'status'}` every 1 s, outside the command driver.
- It has three states: on, off, and error. Error means `running: false` with a non-empty `last_error`, and the tooltip shows the error.

**Keymap**

Keys are ignored while focus is in an input, select or textarea, and when `e.repeat` is set.

| Key | Action | When |
|---|---|---|
| Space | End the phase early or start the next episode | Active |
| Esc | Stop the session (or close the overlay) | Active |
| 1–9 | Select a task (queued while recording) | Any |
| T | Toggle teleop (an emergency stop while recording) | Any |
| H | Home | Not recording |
| Enter | Start the session | Idle, with a valid form |
| ? | Toggle the shortcut overlay | Any |

**`localStorage`**

- Keys: `tasks` (string[]), `activeTask`, `teleopName`, and `homeSet` (string[]).
- Wrap every read and write in try/catch, the same as `settings`.

## Command driver

The single 100 ms loop stays the only place that sends machine commands. It gains two request flags, `teleopRequested` and `homeRequested`. Each tick handles at most one request, in this priority order:

1. **stop**
2. **teleop**: send the toggle. If the phase is `recording`, stop teleop first, then run `apply(cap.next(...))`. Capture ends even if the teleop call fails.
3. **home**: run `home()` with `busy = true`. A reset timer that expires during homing waits, so capture never starts mid-move.
4. **advance**
5. **tick**

## Failure handling

| Case | Behavior |
|---|---|
| Home fails (a move error or a timeout) | Show a banner that names the saver. Teleop stays off. The session stays in reset, so the operator can retry H. |
| Teleop toggle fails | Show a banner. The next status poll sets the pill; the app never assumes the result. |
| Teleop stops itself during recording | Show a red pill and a warning banner. The episode continues; the operator ends it with Space or T. |
| Connection lost | Same as v1: the session stops. Homing in flight is abandoned and its error is shown. |
| No teleop or no savers found | The T and H controls are disabled, with a one-line hint. The v1 capture flow is unaffected. |
| The active task is deleted | Select the first remaining task. Start is disabled when the list is empty. |

## Testing

`src/capture.test.ts` (node:test, fake clock) covers:

- `start` tags with the given task.
- `setTask` during recording leaves the open episode's tag alone, and the next `start_capture` uses the new task.
- `counts` increments on `next` and on `stop` during recording.
- `counts` is unchanged on `stop` during reset.

A new test file for `src/robot.ts` covers `home()` with fake clients:

- With teleop on: stop, then the moves, then start.
- With teleop off: no restart.
- When a move fails: no restart, and the error propagates.

Manual end-to-end on pi-arm6:

1. Set up three tasks and run 4 episodes, swapping tasks mid-session.
2. Press H during a reset.
3. Press T mid-episode.
4. Check Viam Data: there should be 4 sequences with the correct `task:` tags, and the arms should be at home after H.
