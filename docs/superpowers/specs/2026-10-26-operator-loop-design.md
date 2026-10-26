# capture-control-webapp v2 — Operator Loop Design (spec A)

v2 speeds up the operator loop. On top of the v0.1.0 recorder it adds:

- saved tasks you can swap between episodes,
- a teleop toggle,
- home-pose resets,
- more keyboard controls.

The rest of the v2 backlog belongs to other specs: spec B covers episode review, and spec C covers session notes, which are deferred.

Wireframe: https://claude.ai/artifact/6ssrz5AFbcnER8fmgwde25. It predates the review changes listed in the Decisions table below.

## Goals

- Record demos for several subtasks in one session. Between episodes, the operator picks the task the next episode records.
- Toggle the `devrel:so101:teleop` service from the app. While recording, T works as an emergency stop.
- Move the leader and follower arms to a saved home pose on demand, using `erh:vmodutils:arm-position-saver` switches.
- Drive the whole loop from the keyboard.

## Non-goals

- Discarding or retagging an episode, including one ended by an emergency T. That is spec B.
- Sharing tasks across devices. Tasks live in `localStorage`.
- Per-task episode targets, task-specific home poses, and renaming tasks. Delete and re-add instead, because counts are keyed by task name.
- Choosing between several teleop services.
- Homing automatically on every reset. Home runs only when the operator asks for it.

## Decisions

| Topic | Decision |
|---|---|
| Subtask model | A swap palette. A session mixes tasks, and each episode is tagged with the task that was active when it started. |
| Episode count | "Episodes N" stays the session total. Each task chip shows its own count. |
| Task storage | `localStorage`, with add and delete inline. |
| Home | Stop teleop, then move every saver in the home set in parallel. **Teleop stays off afterwards.** The operator presses T once their hand is on the leader. (From review: restarting teleop automatically disables leader torque. The arms are aligned at that point, so the follower streams immediately with no speed cap, and an unheld leader drops and the follower copies the fall.) |
| Home trigger | The H key or the Home button. Not available while recording. |
| Home set | Picked in the app, using checkboxes over the discovered savers. Requires the teleop service's `manage_leader_torque` to be `true` (the default). Without it, the leader may have no torque and can't be moved. |
| Teleop service | Used automatically when exactly one is found. With none or several, T is disabled and a hint says why. With several, Home is disabled as well, because the app can't stop "the" teleop before it moves the arms. While none is found, discovery is retried every 10 s, since without a teleop service there is no teleop-off gate. T is ignored while homing. |
| T while recording | An emergency stop. It sends teleop `stop` (no status read first), then ends the episode. |
| **Teleop-off gate** | If a teleop service exists and isn't running, an expiring reset timer doesn't start the next episode. The reset is **held open** (it shows elapsed time) until the operator presses Space. This one rule covers an emergency T, a home, a failed home, and teleop stopping itself. |

## Session engine (`src/capture.ts`, pure)

- `Settings` loses `task`.
- `Session` gains two fields:
  - `episodeTask`: the tag of the open (or last) episode.
  - `counts: Record<string, number>`.
- `start(settings, task, id, now)` replaces `start(settings, id, now)`.
- `next(s, now, task = s.episodeTask)` and `tick(s, now, task?)` take the task to use if this transition starts an episode. The UI always passes the operator's current pick. There's no task state in the engine to keep in sync: a pick made mid-episode only takes effect when the next episode starts.
- When a recording ends, through `next` or `stop`, the engine runs `counts[episodeTask]++`. A sequence was written in both cases. A `stop` during reset leaves the counts unchanged.
- New: `hold(s)`. During reset it sets `endsAt = Infinity`, so only `next()` (Space) advances. The Recorder uses it for the teleop-off gate.

## Robot helpers (`src/robot.ts`, new, no SDK imports)

Discovery stays in `Recorder.svelte` as effects, matching the existing controller discovery. `robot.ts` holds the pure parts, so they can be tested with fakes.

- **`asTeleopStatus(r)`** normalizes `{running, last_error}`. It returns `undefined` for anything else, which is how a teleop service is recognized.
- **`teleopState(st)`** returns `'on'`, `'off'` or `'error'`. Error means `running: false` with a `last_error`.
- **`isSaver(labels)`** checks that the labels are exactly `['idle', 'update config', 'go to']`.
- **`withTimeout(p, ms, what)`**. Every teleop call times out after 3 s, and every saver move after 30 s. That way a dropped connection or a hung module can't leave the command driver stuck in `busy`.
- **`home(teleop, savers)`**:
  1. Send teleop `stop`. It's idempotent, and it restores leader torque. If it fails, throw and **don't move**.
  2. Run `setPosition(2)` on every saver, with `Promise.allSettled`, so every arm finishes or fails before `home` returns.
  3. Throw an error that names each failed saver. Teleop stays off either way.

  The server keeps running a move even if the app gives up on it. Esc can't stop an arm that is already moving.

## UI (`src/Recorder.svelte`)

The layout is unchanged: the camera grid on top, the drawer at the bottom.

**Drawer, idle:**
1. The task palette replaces the Task input. It shows chips numbered 1–9, each with a delete button, plus an "add task" input.
2. The teleop pill and the Home button, with a one-line hint when either is unavailable.
3. The existing form.
4. A collapsed **Robot setup** section with a home-set checkbox for each discovered saver.

**Drawer, active:**
- The status line.
- The palette. Chips show counts, and a queued pick is drawn dashed with a "next" label.
- The teleop pill and the Home button, with Home disabled while recording.
- A hint line that lists the keys.

**Cameras while homing:** an overlay reads "Homing …".

**Teleop pill:**
- Polls `{command: 'status'}` every 1 s, outside the driver.
- A sequence counter discards poll results that started before a toggle or a home.
- After a successful toggle or home, the pill is set from the command that was sent.

**Keymap:**
- Keys are ignored when the focus is in an input, select or textarea.
- They are also ignored on key repeat, and when Ctrl, Meta or Alt is held.

| Key | Action | When |
|---|---|---|
| Space | End the phase early, or start the next episode | Active |
| Esc | Close the overlay, or else stop the session | Active, or overlay open |
| 1–9 | Select a task (queued while recording) | Any |
| T | Toggle teleop (emergency stop while recording) | Teleop available |
| H | Home | Not recording |
| Enter | Start the session (same guard as the Start button; ignored on buttons, summary and links) | Idle |
| ? | Toggle the shortcut overlay | Any |

**`localStorage`:** the keys are `tasks` (string[]), `activeTask` and `homeSet` (string[]). Every read and write goes through try/catch.

## Command driver

The single 100 ms loop remains the only sender of machine commands, and it is the only code that writes `session`.

- **What runs when.** Teleop and home requests run whenever the driver isn't `busy`. Stop, advance and tick run only during an active session.
- **Priority:** stop, then teleop, then home, then advance, then tick.
- **Teleop.** While recording, it sends `stop` and then `apply(cap.next(...))`. Capture ends even if the teleop call fails or times out. Outside recording, it reads the status and sends the opposite command.
- **Home.** Runs `home()` with `busy` set. When it finishes, the reset is held open whether the home succeeded or failed.
- **Tick.** When the reset timer expires while the teleop-off gate is shut, the driver calls `session = cap.hold(session)` instead of starting capture.
- **`startSession`** clears all request flags and checks the same conditions as the Start button. `requestSubmit()` doesn't respect a disabled submit button, so the check has to live in `startSession` itself.
- **When a capture command fails,** `apply` sets the session to `{...nextSession, phase: 'done'}` instead of calling `cap.stop(nextSession)`. Calling `cap.stop` would count an episode whose `start_capture` never succeeded.

## Failure handling

| Case | Behavior |
|---|---|
| Home fails or times out | Show a banner naming the savers. Teleop stays off, and the reset is held open. Retry with H. |
| Teleop stop fails during home | Show a banner. Don't move any arm. |
| Teleop toggle fails or times out | Show a banner. The next status poll sets the pill. |
| Emergency T, teleop call fails | The episode still ends, and the banner shows the teleop error. |
| Teleop stops itself during recording | The pill turns red and a warning banner appears. The episode continues until Space or T, and the next reset is held open. |
| Connection lost | Same as v1: the session stops. A call in flight times out and frees the driver. |
| No teleop service, or several | T is disabled, with a hint. There's no gate, so the v1 timed flow is unchanged. |
| No savers found | H is disabled, with a hint. |
| The active task is deleted | Select the first remaining task. Start is disabled when there are no tasks. |

## Testing

`src/capture.test.ts` (node:test, fake clock):
- `start` tags with the given task.
- A pick passed to `next` during reset tags the next episode, and the open episode keeps its tag.
- Counts increment on `next` and on `stop` during recording, and stay the same on `stop` during reset.
- `hold` makes the reset open-ended, and `next` still advances it.

`src/robot.test.ts`:
- `home` stops teleop, then moves every saver.
- Without a teleop service, `home` just moves.
- A failed move names the saver, after every move has settled.
- A failed teleop stop means no move.
- A move timeout rejects.
- Status and saver detection.

Manual end-to-end check on pi-arm6:
1. Run 4 episodes across 3 tasks, swapping tasks mid-session.
2. Press H during a reset. The arms go home, teleop stays off, and the reset is held.
3. Press T, then Space.
4. Press T mid-episode.
5. Check Viam Data: there should be 4 sequences with the correct `task:` tags.
