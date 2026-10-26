# Operator Loop (v2 spec A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add four things to the operator loop:
- Saved tasks you can swap between episodes.
- A teleop toggle, where T also works as an emergency stop.
- An on-demand home-pose reset that leaves teleop off afterwards.
- A richer keymap.

**Spec:** `docs/superpowers/specs/2026-10-26-operator-loop-design.md`. Read it first, especially the **Decisions** table and the **teleop-off gate**.

**Architecture:** Almost everything is frontend.
- `src/capture.ts` stays pure. `start` takes a task. `next` and `tick` take the task to use if they start an episode. The engine counts episodes per task. A new `hold()` makes a reset open-ended.
- `src/robot.ts` is new and has no Svelte or SDK imports. It identifies teleop services and arm-position-saver switches from their responses, runs `home()` (stop teleop, then move all savers) with timeouts, and works against small structural interfaces so `node:test` can use fakes.
- `src/Recorder.svelte` does the wiring. Its existing 100 ms command driver stays the only code that sends machine commands or writes `session`. It also handles teleop and home requests, and it holds a reset open instead of auto-starting an episode while teleop is off.

**Tech Stack:**
- Svelte 5 runes.
- `@viamrobotics/sdk` 0.76: `GenericServiceClient` and `SwitchClient`.
- `@viamrobotics/svelte-sdk` 1.5: `useResourceNames`.
- `node:test`, run with Node 24's type stripping.

**Facts the engineer needs:**
- **Teleop service.** `devrel:so101:teleop` is a generic service.
  - `doCommand({command: 'status'})` returns `{running: boolean, last_error: string, …}`.
  - `{command: 'start'}` and `{command: 'stop'}` are idempotent.
  - `stop` restores the leader arm's torque.
- **Home poses.** `erh:vmodutils:arm-position-saver` is a **switch** component.
  - `getNumberOfPositions()` returns `[3, ['idle', 'update config', 'go to']]`.
  - `setPosition(2)` moves the arm to the saved pose and resolves once the move is done.
- **`useResourceNames(partID, subtype)`** filters by `subtype` only. Generic *components* share the `generic` subtype, so also filter on `r.type === 'service'`.
- **Disconnects.** The svelte-sdk resets its queries on disconnect, so guard on `query?.isSuccess`, the same way the existing code does.
- **Commands.**
  - `npm test` runs `node --test`.
  - `npm run check` runs svelte-check.
  - `npm run build` runs `vite build`.

**Plan review:** This plan has been through an Opus review, which applied it to a scratch copy. The fixes from that review are already folded in. Don't reintroduce:
- an automatic teleop restart after Home;
- a status read before the emergency stop;
- `Promise.all` for the moves;
- `cap.stop(nextSession)` in `apply`'s catch.

---

## File map

| File | Change |
|---|---|
| `src/capture.ts` | `Settings` loses `task`. `Session` gains `episodeTask` and `counts`. `start` and `next` take a task, `tick` takes an optional one. New `hold`. |
| `src/capture.test.ts` | Update for the new `start` signature, and add tests for task, counts and hold. |
| `src/robot.ts` | New: `asTeleopStatus`, `teleopState`, `isSaver`, `withTimeout`, `home`, `TELEOP_TIMEOUT_MS`. |
| `src/robot.test.ts` | New. |
| `package.json` | The `test` script becomes `node --test src/*.test.ts`. |
| `src/Recorder.svelte` | Task palette, teleop pill and gate, Home with Robot setup, keymap, shortcut overlay, driver changes. |
| `README.md` | Teleop and home setup, and the keymap. |

Task 1 changes the signature of `start`, so `npm run check` fails until Task 2 is done. In Task 1, run only `npm test`.

---

### Task 1: Engine: per-episode task, per-task counts, `hold` (TDD)

**Files:**
- Modify: `src/capture.ts`
- Test: `src/capture.test.ts`

- [ ] **Step 1: Update the existing tests for the new `start` signature**

In `src/capture.test.ts`, remove `task` from the `settings` constant:

```ts
const settings: cap.Settings = { episodes: 2, episodeS: 10, resetS: 5, fps: 15 };
```

Then pass the task explicitly. `START` already expects `task:pick cube`.

```bash
sed -i '' "s/cap.start(settings, 'abc', 0)/cap.start(settings, 'pick cube', 'abc', 0)/; s/cap.start(open, 'abc', 0)/cap.start(open, 'pick cube', 'abc', 0)/" src/capture.test.ts
```

The existing tests call `next(s, now)` and `tick(s, now)` without a task. Those calls must keep working, reusing the last episode's task.

- [ ] **Step 2: Add the failing tests**

Add these at the end of `src/capture.test.ts`:

```ts
test('the task passed when an episode starts tags it; counts per task', () => {
  let [s] = cap.start(settings, 'reach', 'abc', 0);
  let cmds: cap.Command[];

  [s, cmds] = cap.next(s, 1_000, 'grasp'); // ends 'reach'; the pick only matters when an episode starts
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.episodeTask, 'reach');
  assert.deepEqual(s.counts, { reach: 1 });

  [s, cmds] = cap.tick(s, 6_000, 'grasp'); // reset expires
  assert.deepEqual(cmds, [{ start_capture: true, frequency_hz: 15, tags: ['task:grasp', 'session:abc', 'fps:15'] }]);
  assert.equal(s.episodeTask, 'grasp');

  [s] = cap.next(s, 7_000, 'place'); // last of 2 episodes → done
  assert.equal(s.phase, 'done');
  assert.deepEqual(s.counts, { reach: 1, grasp: 1 });
});

test('stop() counts the open episode; stop() during reset does not', () => {
  let [s] = cap.start(settings, 'reach', 'abc', 0);
  assert.deepEqual(cap.stop(s)[0].counts, { reach: 1 });

  [s] = cap.next(s, 1_000); // reset; reach counted once
  assert.deepEqual(cap.stop(s)[0].counts, { reach: 1 });
});

test('hold() keeps the reset open until next(); no-op while recording', () => {
  let [s] = cap.start(settings, 'reach', 'abc', 0);
  assert.equal(cap.hold(s), s);

  [s] = cap.next(s, 1_000);
  s = cap.hold(s);
  let cmds: cap.Command[];
  [s, cmds] = cap.tick(s, 1e9, 'grasp');
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'reset');
  assert.equal(cap.remainingMs(s, 1e9), Infinity);

  [s, cmds] = cap.next(s, 1e9, 'grasp');
  assert.equal(s.phase, 'recording');
  assert.equal(s.episodeTask, 'grasp');
});
```

- [ ] **Step 3: Run the tests and confirm they fail**

Run: `npm test`
Expected: FAIL. `cap.hold is not a function`, the `counts` assertions fail, and the `task:grasp` tag assertion fails.

- [ ] **Step 4: Implement**

In `src/capture.ts`, replace everything from `export type Settings` down to, but not including, `export function start`. That region covers the types, `STOP`, `tags`, `startCapture` and `until`. Replace it with:

```ts
export type Settings = { episodes: number; episodeS: number | null; resetS: number; fps: number };
export type Phase = 'recording' | 'reset' | 'done';
// startedAt/endsAt bound the current phase; endsAt is Infinity for an open-ended phase.
// episodeTask tags the open (or last) episode; counts holds ended episodes per task.
export type Session = {
  id: string;
  settings: Settings;
  phase: Phase;
  episode: number;
  startedAt: number;
  endsAt: number;
  episodeTask: string;
  counts: Record<string, number>;
};
export type Command =
  | { start_capture: true; frequency_hz: number; tags: string[] }
  | { stop_capture: true };
export type Step = [Session, Command[]];

const STOP: Command = { stop_capture: true };

// frequency_hz must always be sent: the sensor's default_capture_frequency_hz is 0 (= no capture).
function startCapture(s: Session): Command {
  return {
    start_capture: true,
    frequency_hz: s.settings.fps,
    tags: [`task:${s.episodeTask}`, `session:${s.id}`, `fps:${s.settings.fps}`],
  };
}

// Every ended recording wrote a sequence, so it counts toward its task.
const counted = (s: Session): Record<string, number> => ({ ...s.counts, [s.episodeTask]: (s.counts[s.episodeTask] ?? 0) + 1 });

// null seconds = open-ended: the phase only ends via next() (space bar).
const until = (now: number, seconds: number | null): number => (seconds == null ? Infinity : now + seconds * 1000);
```

Replace `start`, `next`, `tick` and `stop`, and add `hold`:

```ts
export function start(settings: Settings, task: string, id: string, now: number): Step {
  const s: Session = {
    id, settings, phase: 'recording', episode: 1, startedAt: now, endsAt: until(now, settings.episodeS),
    episodeTask: task, counts: {},
  };
  return [s, [startCapture(s)]];
}

/**
 * Ends the current phase now (space bar). The last episode skips reset and goes to done.
 * task tags the episode this starts, if any; the open episode keeps its own tag.
 */
export function next(s: Session, now: number, task: string = s.episodeTask): Step {
  if (s.phase === 'recording') {
    const counts = counted(s);
    if (s.episode >= s.settings.episodes) return [{ ...s, phase: 'done', counts }, [STOP]];
    return [{ ...s, phase: 'reset', counts, startedAt: now, endsAt: until(now, s.settings.resetS) }, [STOP]];
  }
  if (s.phase === 'reset') {
    const r: Session = {
      ...s,
      phase: 'recording',
      episode: s.episode + 1,
      episodeTask: task,
      startedAt: now,
      endsAt: until(now, s.settings.episodeS),
    };
    return [r, [startCapture(r)]];
  }
  return [s, []];
}

export function tick(s: Session, now: number, task?: string): Step {
  return now >= s.endsAt ? next(s, now, task) : [s, []];
}

/** Holds a reset open until next() (space): used while teleop is off, so no episode auto-starts. */
export function hold(s: Session): Session {
  return s.phase === 'reset' ? { ...s, endsAt: Infinity } : s;
}

/** Stops the session. Capture is already stopped during reset, so only recording needs stop_capture. */
export function stop(s: Session): Step {
  if (s.phase === 'recording') return [{ ...s, phase: 'done', counts: counted(s) }, [STOP]];
  return [{ ...s, phase: 'done' }, []];
}
```

`next(s, now, undefined)` falls back to the default, so `tick` with no task reuses `episodeTask`. Leave `remainingMs`, `elapsedMs` and the readings helpers unchanged.

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `npm test`
Expected: 8 tests pass.

- [ ] **Step 6: Commit**

```bash
git add src/capture.ts src/capture.test.ts
git commit -m "feat(engine): per-episode task, per-task counts, hold()"
```

---

### Task 2: Task palette in the Recorder

**Files:**
- Modify: `src/Recorder.svelte`

- [ ] **Step 1: Storage helpers and task state**

Replace `DEFAULTS` and `loadSettings()` with:

```ts
  function load<T>(key: string, fallback: T): T {
    try {
      const v = localStorage.getItem(key);
      return v == null ? fallback : JSON.parse(v);
    } catch {
      return fallback;
    }
  }
  function save(key: string, value: unknown) {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch {}
  }

  const DEFAULTS: cap.Settings = { episodes: 10, episodeS: null, resetS: 10, fps: 10 };
  const { task: v1Task, ...storedSettings } = load<Partial<cap.Settings> & { task?: string }>('settings', {}); // v1 kept the task in settings
```

Change the `settings` declaration to:

```ts
  let settings = $state<cap.Settings>({ ...DEFAULTS, ...storedSettings });
```

Below the other `$state` declarations, add:

```ts
  // Saved tasks (localStorage). activeTask is what the next episode records.
  let tasks = $state<string[]>(load('tasks', v1Task ? [v1Task] : []));
  let activeTask = $state<string>(load('activeTask', ''));
  let formEl = $state<HTMLFormElement>();
  $effect(() => save('tasks', $state.snapshot(tasks)));
  $effect(() => save('activeTask', activeTask));
  $effect(() => {
    if (!tasks.includes(activeTask)) activeTask = tasks[0] ?? '';
  });
```

Below `const active = …`, add:

```ts
  // While recording, the open episode's chip is highlighted; a different pick shows as queued ("next").
  const shownTask = $derived(session?.phase === 'recording' ? session.episodeTask : activeTask);
  const queued = $derived(session?.phase === 'recording' && activeTask !== session.episodeTask);
  const canStart = $derived(!!sensor && !orphaned && !!activeTask);
```

- [ ] **Step 2: Palette actions**

Below `stopOrphan()`, add:

```ts
  function selectTask(i: number) {
    if (tasks[i]) activeTask = tasks[i];
  }

  function addTask(e: KeyboardEvent & { currentTarget: HTMLInputElement }) {
    if (e.key !== 'Enter') return;
    const t = e.currentTarget.value.trim();
    if (!t) return;
    if (!tasks.includes(t)) tasks.push(t);
    activeTask = t;
    e.currentTarget.value = '';
  }
```

Deleting a task is done inline in the markup with `tasks.splice(i, 1)`. There's no rename: delete and add instead.

- [ ] **Step 3: Start with the active task, pass the pick in the driver, and fix the counts on failure**

In `startSession()`:
- Make the first line `if (busy || !canStart) return;`. `requestSubmit()` from the Enter key ignores the disabled button, so the guard has to live here.
- Replace the `localStorage.setItem` try/catch with `save('settings', snapshot);`.
- Replace the `apply(...)` call with:

```ts
    apply(cap.start(snapshot, activeTask, crypto.randomUUID().slice(0, 8), Date.now()));
```

In `apply()`'s `catch`, replace `session = cap.stop(nextSession)[0];` with:

```ts
      // nextSession already counts every episode that ended; a failed start_capture never opened one.
      session = { ...nextSession, phase: 'done' };
```

In the `setInterval` driver in `onMount`, pass the operator's pick:
- Change `apply(cap.next(session, now))` to `apply(cap.next(session, now, activeTask))`.
- Change `apply(cap.tick(session, now))` to `apply(cap.tick(session, now, activeTask))`.

- [ ] **Step 4: Keys 1–9 and Enter; ignore keys in fields and keys with modifiers**

Replace `onKey` with:

```ts
  function onKey(e: KeyboardEvent) {
    if (e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target instanceof Element && e.target.closest('input, select, textarea')) return;
    if (/^[1-9]$/.test(e.key)) {
      selectTask(Number(e.key) - 1);
    } else if (e.key === 'Enter' && !active && !(e.target instanceof Element && e.target.closest('button, summary, a'))) {
      formEl?.requestSubmit();
    } else if (active && e.code === 'Space') {
      e.preventDefault();
      advanceRequested = true;
    } else if (active && e.key === 'Escape') {
      stopRequested = true;
    }
  }
```

- [ ] **Step 5: Markup**

Just inside `<main>`, before the cams `<section>`, add a palette snippet:

```svelte
  {#snippet palette()}
    <div class="palette" role="group" aria-label="Tasks">
      {#each tasks as t, i (t)}
        <span class="chip" class:active={t === shownTask} class:queued={queued && t === activeTask}>
          <button type="button" onclick={() => selectTask(i)}>
            {#if i < 9}<kbd>{i + 1}</kbd>{/if}
            {t}
            {#if session}<small>×{session.counts[t] ?? 0}</small>{/if}
            {#if queued && t === activeTask}<em>next</em>{/if}
          </button>
          {#if !active}<button type="button" class="x" aria-label="Delete {t}" onclick={() => tasks.splice(i, 1)}>×</button>{/if}
        </span>
      {/each}
      <input class="add" placeholder="+ add task" aria-label="Add task" onkeydown={addTask} />
    </div>
  {/snippet}
```

In the active branch, change `· task:{session.settings.task}` to `· task:{session.episodeTask}`, and add `{@render palette()}` after the `.status` div.

In the done line (`<p class="last">`), remove ` · task:{session.settings.task}`.

In the idle branch:
- Add `{@render palette()}` before the `<form>`.
- Add `bind:this={formEl}` to the `<form>`.
- Delete the `<label class="wide">Task …</label>` line.
- Change the submit button to `disabled={!canStart}`.

Add the styles:

```css
  .palette { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .chip { display: inline-flex; align-items: center; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .chip button { border: 0; background: none; padding: 4px 10px; display: inline-flex; gap: 6px; align-items: baseline; cursor: pointer; }
  .chip button.x { padding: 4px 8px 4px 0; color: var(--muted); }
  .chip.active { border-color: currentColor; font-weight: 600; }
  .chip.queued { border-style: dashed; }
  .chip kbd, .chip small { font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .chip em { font-size: 10px; font-style: normal; text-transform: uppercase; letter-spacing: 0.05em; }
  .add { width: 150px; border: 1px dashed var(--border); border-radius: 999px; }
```

Delete the now-unused `label.wide` rule.

- [ ] **Step 6: Check, test, build**

Run: `npm run check && npm test && npm run build`
Expected: 0 errors, all tests pass, and the build succeeds.

- [ ] **Step 7: Manual smoke test (no machine needed for the palette)**

1. Run `npm run dev`.
2. Add `reach`, `grasp` and `place`.
3. Press 2 with no field focused. `grasp` becomes active.
4. Delete `place`.
5. Reload. The tasks and the active task persist.

- [ ] **Step 8: Commit**

```bash
git add src/Recorder.svelte
git commit -m "feat(ui): saved task palette with 1-9 swap and Enter to start"
```

---

### Task 3: `robot.ts` helpers (TDD)

**Files:**
- Create: `src/robot.ts`, `src/robot.test.ts`
- Modify: `package.json`

- [ ] **Step 1: Test script**

In `package.json`, set `"test": "node --test src/*.test.ts"`.

- [ ] **Step 2: Write the failing tests**

Create `src/robot.test.ts`:

```ts
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as rb from './robot.ts';

function fakeTeleop(log: string[], failStop = false): rb.DoCommander {
  return {
    async doCommand(cmd) {
      log.push(`teleop:${cmd.command}`);
      if (failStop && cmd.command === 'stop') throw new Error('unreachable');
      return { running: false };
    },
  };
}

const saver = (name: string, log: string[], behavior: 'ok' | 'fail' | 'hang' = 'ok'): rb.Saver => ({
  name,
  setPosition(p) {
    log.push(`${name}:${p}`);
    if (behavior === 'hang') return new Promise(() => {});
    return behavior === 'fail' ? Promise.reject(new Error('stall')) : Promise.resolve();
  },
});

test('home stops teleop, moves every saver, and leaves teleop off', async () => {
  const log: string[] = [];
  await rb.home(fakeTeleop(log), [saver('leader-home', log), saver('follower-home', log)]);
  assert.deepEqual(log, ['teleop:stop', 'leader-home:2', 'follower-home:2']);
});

test('home without a teleop service just moves', async () => {
  const log: string[] = [];
  await rb.home(undefined, [saver('leader-home', log)]);
  assert.deepEqual(log, ['leader-home:2']);
});

test('a failed teleop stop moves nothing', async () => {
  const log: string[] = [];
  await assert.rejects(rb.home(fakeTeleop(log, true), [saver('leader-home', log)]), /unreachable/);
  assert.deepEqual(log, ['teleop:stop']);
});

test('a failed move names the saver after every move settles', async () => {
  const log: string[] = [];
  await assert.rejects(
    rb.home(undefined, [saver('leader-home', log, 'fail'), saver('follower-home', log)]),
    /leader-home \(stall\)/,
  );
  assert.deepEqual(log, ['leader-home:2', 'follower-home:2']);
});

test('a hung move times out', async () => {
  await assert.rejects(rb.home(undefined, [saver('leader-home', [], 'hang')], 20), /leader-home \(.*timed out/);
});

test('status and saver detection', () => {
  assert.deepEqual(rb.asTeleopStatus({ running: true, last_error: '', cycles: 3 }), { running: true, last_error: '' });
  assert.equal(rb.asTeleopStatus({ ok: true }), undefined);
  assert.equal(rb.asTeleopStatus(null), undefined);
  assert.equal(rb.teleopState({ running: true, last_error: '' }), 'on');
  assert.equal(rb.teleopState({ running: false, last_error: '' }), 'off');
  assert.equal(rb.teleopState({ running: false, last_error: 'servo 3 timeout' }), 'error');
  assert.equal(rb.teleopState(undefined), 'off');
  assert.equal(rb.isSaver(['idle', 'update config', 'go to']), true);
  assert.equal(rb.isSaver(['off', 'on']), false);
});
```

- [ ] **Step 3: Run the tests and confirm they fail**

Run: `npm test`
Expected: FAIL with `Cannot find module './robot.ts'`.

- [ ] **Step 4: Implement**

Create `src/robot.ts`:

```ts
// Robot-side helpers for the devrel:so101:teleop service and erh:vmodutils:arm-position-saver
// switches. No SDK imports: callers pass SDK clients, which match these shapes.

export type DoCommander = { doCommand(cmd: { command: string }): Promise<unknown> };
export type Saver = { name: string; setPosition(position: number): Promise<void> };
export type TeleopStatus = { running: boolean; last_error: string };

export const TELEOP_TIMEOUT_MS = 3_000;
const MOVE_TIMEOUT_MS = 30_000;
const GO_TO = 2; // arm-position-saver: 0 idle, 1 save current pose, 2 go to saved pose

export function asTeleopStatus(r: unknown): TeleopStatus | undefined {
  if (typeof r !== 'object' || r === null || typeof (r as { running?: unknown }).running !== 'boolean') return undefined;
  const { running, last_error } = r as { running: boolean; last_error?: unknown };
  return { running, last_error: typeof last_error === 'string' ? last_error : '' };
}

/** error = the loop stopped itself (max_consecutive_errors) and recorded why. */
export function teleopState(st: TeleopStatus | undefined): 'on' | 'off' | 'error' {
  if (st?.running) return 'on';
  return st?.last_error ? 'error' : 'off';
}

const SAVER_LABELS = ['idle', 'update config', 'go to'];
export const isSaver = (labels: string[]): boolean => labels.join('\n') === SAVER_LABELS.join('\n');

/** Rejects if p hasn't settled within ms, so a dropped connection can't wedge the command driver. */
export async function withTimeout<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what} timed out after ${ms / 1000} s`)), ms);
  });
  try {
    return await Promise.race([p, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Stops teleop (restoring leader torque), then moves every saver to its pose in parallel.
 * Teleop stays off: restarting it would drop leader torque while nobody may be holding it,
 * and the follower would copy the fall at full streaming speed. The operator presses T.
 */
export async function home(teleop: DoCommander | undefined, savers: Saver[], moveTimeoutMs = MOVE_TIMEOUT_MS): Promise<void> {
  if (teleop) await withTimeout(teleop.doCommand({ command: 'stop' }), TELEOP_TIMEOUT_MS, 'Teleop stop');
  // allSettled: every arm finishes (or fails) before we report, so nothing is still moving when busy clears.
  const results = await Promise.allSettled(savers.map((s) => withTimeout(s.setPosition(GO_TO), moveTimeoutMs, 'move')));
  const failed = savers.flatMap((s, i) => {
    const r = results[i];
    return r.status === 'rejected' ? [`${s.name} (${r.reason instanceof Error ? r.reason.message : r.reason})`] : [];
  });
  if (failed.length) throw new Error(`Home failed: ${failed.join(', ')}. Teleop is off.`);
}
```

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `npm test`
Expected: all tests in both files pass, and the run finishes in well under a second. No 30 s timer is left pending, because `withTimeout` clears its timer.

- [ ] **Step 6: Commit**

```bash
git add package.json src/robot.ts src/robot.test.ts
git commit -m "feat: robot helpers for teleop status, saver detection, timed home"
```

---

### Task 4: Teleop: discovery, pill, T, and the teleop-off gate

**Files:**
- Modify: `src/Recorder.svelte`

- [ ] **Step 1: Imports and state**

```ts
  import { GenericServiceClient, MachineConnectionEvent, SensorClient, SwitchClient } from '@viamrobotics/sdk';
  import * as rb from './robot.ts';
```

`SwitchClient` is used in Task 5. Importing it now is harmless.

Below `const cameras = …`, add `const generics = useResourceNames(() => partID, 'generic');`.

Below the task state, add:

```ts
  let teleops = $state<string[]>([]);
  let teleopStatus = $state<rb.TeleopStatus | undefined>();
  let teleopRequested = false;
  let pollSeq = 0; // bumped by toggles and homes so an older in-flight status poll can't overwrite the newer state
  // Re-runs teleop discovery while none is found: no teleop means no teleop-off gate, so a slow start must not stick.
  let discoverTick = $state(0);
```

Below the `sensor` derived, add:

```ts
  // Exactly one teleop service is used automatically; with none or several, T is disabled.
  const teleop = $derived(robot.current && teleops.length === 1 ? new GenericServiceClient(robot.current, teleops[0]) : undefined);
  const teleopMode = $derived(rb.teleopState(teleopStatus));
```

- [ ] **Step 2: Discovery effect**

Add this below the controller discovery effect:

```ts
  // Discover teleop services: generic services that answer {command: 'status'} with a boolean `running`.
  $effect(() => {
    const client = robot.current;
    const names = generics.current.filter((r) => r.type === 'service').map((r) => r.name);
    discoverTick; // dependency: retry trigger from the status poll
    if (!client || !connected || !generics.query?.isSuccess) return;
    Promise.all(
      names.map(async (name) => {
        try {
          const r = await rb.withTimeout(new GenericServiceClient(client, name).doCommand({ command: 'status' }), rb.TELEOP_TIMEOUT_MS, name);
          return rb.asTeleopStatus(r) ? name : undefined;
        } catch {
          return undefined;
        }
      }),
    ).then((found) => (teleops = found.filter((n) => n !== undefined)));
  });
```

- [ ] **Step 3: Toggle**

Add this below `apply()`:

```ts
  // Runs only from the driver. While recording, T is an emergency stop: send stop with no status
  // read first, then end the episode. Ending capture never depends on the teleop call.
  async function toggleTeleop() {
    const t = teleop;
    if (!t) return;
    const recording = session?.phase === 'recording';
    const call = (command: string) => rb.withTimeout(t.doCommand({ command }), rb.TELEOP_TIMEOUT_MS, `Teleop ${command}`);
    busy = true;
    pollSeq++;
    try {
      const command = recording || rb.asTeleopStatus(await call('status'))?.running ? 'stop' : 'start';
      await call(command);
      teleopStatus = { running: command === 'start', last_error: '' };
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      pollSeq++; // also drop polls that were answered before the command took effect
      busy = false;
    }
    if (recording && session?.phase === 'recording') await apply(cap.next(session, Date.now(), activeTask));
  }
```

- [ ] **Step 4: The driver**

T works when no session is running, so the driver has to run when idle too. Replace the `setInterval` callback body with:

```ts
      now = Date.now();
      if (busy) return;
      const live = session && active ? session : undefined;
      if (live && stopRequested) {
        stopRequested = advanceRequested = false;
        await apply(cap.stop(live));
      } else if (teleopRequested) {
        teleopRequested = false;
        await toggleTeleop();
      } else if (live && advanceRequested) {
        advanceRequested = false;
        await apply(cap.next(live, now, activeTask));
      } else if (live) {
        const step = cap.tick(live, now, activeTask);
        // Teleop-off gate: never auto-start an episode with nobody driving. Space still starts it.
        if (live.phase === 'reset' && step[0].phase === 'recording' && teleop && teleopMode !== 'on') session = cap.hold(live);
        else await apply(step);
      }
```

In `startSession()`, change the flag reset to `advanceRequested = stopRequested = teleopRequested = false;`.

- [ ] **Step 5: Status poll**

In `onMount`, after the driver interval, add:

```ts
    // Read-only, so it runs outside the driver. pollSeq drops results that a toggle or home has superseded.
    let polls = 0;
    const poll = setInterval(() => {
      if (!teleops.length && connected && ++polls % 10 === 0) discoverTick++; // ponytail: 10 s retry until found
      const t = teleop;
      const seq = pollSeq;
      if (!t || !connected) return void (teleopStatus = undefined);
      rb.withTimeout(t.doCommand({ command: 'status' }), rb.TELEOP_TIMEOUT_MS, 'Teleop status')
        .then((r) => {
          if (seq === pollSeq) teleopStatus = rb.asTeleopStatus(r);
        })
        .catch(() => {
          if (seq === pollSeq) teleopStatus = undefined;
        });
    }, 1000);
```

Add `clearInterval(poll);` to the cleanup.

- [ ] **Step 6: The T key**

In `onKey`, add this branch before the `Enter` branch:

```ts
    } else if (e.key === 't' || e.key === 'T') {
      if (teleop && !homing) teleopRequested = true;
```

(`homing` is declared in Task 5. In this task, use `if (teleop) teleopRequested = true;`, and Task 6's final `onKey` adds `!homing`. Likewise, the pill's `|| homing` comes in Task 5.)

- [ ] **Step 7: Markup**

Add a snippet next to `palette`:

```svelte
  {#snippet robotControls()}
    <div class="robot">
      <button
        type="button"
        class="pill {teleopMode}"
        disabled={!teleop || homing}
        onclick={() => (teleopRequested = true)}
        title={teleopStatus?.last_error || teleops[0] || ''}
      >
        <span class="dot"></span>Teleop {teleopMode} <kbd>T</kbd>
      </button>
      {#if teleops.length > 1}
        <small class="hint">{teleops.length} teleop services found; T needs exactly one.</small>
      {:else if !teleop}
        <small class="hint">No devrel:so101:teleop service found.</small>
      {/if}
    </div>
  {/snippet}
```

Render `{@render robotControls()}` after `{@render palette()}` in both the active and the idle branch.

Below the `orphaned` banner, add:

```svelte
    {#if session?.phase === 'recording' && teleopMode === 'error'}
      <p class="banner warn" role="alert">Teleop stopped itself: {teleopStatus?.last_error}. Space ends the episode.</p>
    {/if}
```

In the active `.status`, if the reset is being held, show why. Add this after the clock span:

```svelte
        {#if session.phase === 'reset' && !Number.isFinite(cap.remainingMs(session, now))}<span>held, press space to start</span>{/if}
```

Add the styles:

```css
  .robot { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .pill { display: inline-flex; align-items: center; gap: 6px; border-radius: 6px; }
  .pill .dot { width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
  .pill.on .dot { background: var(--done); }
  .pill.error .dot { background: var(--rec); }
  .pill kbd, .hint { font-size: 12px; color: var(--muted); }
```

Before relying on these tokens, check that `--done`, `--rec` and `--muted` exist in `App.svelte`. Use whatever green token is defined there.

- [ ] **Step 8: Check, test, build**

Run: `npm run check && npm test && npm run build`
Expected: 0 errors, all tests pass, and the build succeeds.

- [ ] **Step 9: Commit**

```bash
git add src/Recorder.svelte
git commit -m "feat(ui): teleop pill, T toggle / emergency stop, teleop-off reset gate"
```

---

### Task 5: Home: saver discovery, Robot setup, H

**Files:**
- Modify: `src/Recorder.svelte`

- [ ] **Step 1: State and discovery**

Below `generics`, add `const switches = useResourceNames(() => partID, 'switch');`.

Below the teleop state, add:

```ts
  let savers = $state<string[]>([]);
  let homeSet = $state<string[]>(load('homeSet', []));
  let homing = $state(false);
  let homeRequested = false;
  $effect(() => save('homeSet', $state.snapshot(homeSet)));
```

**Below `const connected = …`** (it must come after `connected`, or svelte-check fails with TS2448), add:

```ts
  const homeSavers = $derived(homeSet.filter((n) => savers.includes(n)));
  // With several teleop services we can't stop "the" teleop before moving, so Home is unavailable.
  const canHome = $derived(connected && homeSavers.length > 0 && teleops.length <= 1 && session?.phase !== 'recording' && !homing);
```

Add the discovery effect:

```ts
  // Discover arm-position-saver switches by their position labels.
  $effect(() => {
    const client = robot.current;
    const names = switches.current.map((r) => r.name);
    if (!client || !connected || !switches.query?.isSuccess) return;
    Promise.all(
      names.map(async (name) => {
        try {
          const [, labels] = await new SwitchClient(client, name).getNumberOfPositions();
          return rb.isSaver(labels) ? name : undefined;
        } catch {
          return undefined;
        }
      }),
    ).then((found) => (savers = found.filter((n) => n !== undefined)));
  });
```

- [ ] **Step 2: `doHome` and the driver branch**

Below `toggleTeleop()`, add:

```ts
  // Runs only from the driver with busy set, so a reset timer that expires mid-move waits.
  async function doHome() {
    const client = robot.current;
    if (!client || !canHome) return;
    busy = homing = true;
    pollSeq++;
    try {
      await rb.home(
        teleop,
        homeSavers.map((name) => ({ name, setPosition: (p: number) => new SwitchClient(client, name).setPosition(p) })),
      );
      if (teleop) teleopStatus = { running: false, last_error: '' };
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      // Teleop is off (or unknown): the operator starts the next episode deliberately with Space.
      if (session?.phase === 'reset') session = cap.hold(session);
      pollSeq++;
      busy = homing = false;
    }
  }
```

In the driver, add this after the `teleopRequested` branch:

```ts
      } else if (homeRequested) {
        homeRequested = false;
        await doHome();
```

In `onKey`, add this after the T branch:

```ts
    } else if (e.key === 'h' || e.key === 'H') {
      if (canHome) homeRequested = true;
```

In `startSession()`, clear this flag too: `advanceRequested = stopRequested = teleopRequested = homeRequested = false;`.

- [ ] **Step 3: Markup**

In `robotControls`, add the following after the teleop hint block, still inside `.robot`:

```svelte
      <button type="button" class="pill" disabled={!canHome} onclick={() => (homeRequested = true)} title={homeSavers.join(', ')}>
        {homing ? 'Homing…' : 'Home'} <kbd>H</kbd>
      </button>
      {#if teleops.length > 1}
        <small class="hint">Home needs at most one teleop service.</small>
      {:else if savers.length === 0}
        <small class="hint">No arm-position-saver switches found.</small>
      {:else if homeSavers.length === 0}
        <small class="hint">Pick a home set in Robot setup.</small>
      {/if}
```

Inside the cams `<section>`, after the badge, add:

```svelte
    {#if homing}<div class="homing" aria-live="polite">Homing {homeSavers.join(', ')}…</div>{/if}
```

In the idle branch, after `</form>`, add:

```svelte
      <details class="setup">
        <summary>Robot setup: home set {homeSavers.length} of {savers.length}</summary>
        <fieldset>
          <legend>Home set (arm-position-saver switches). Home stops teleop, moves these, and leaves teleop off.</legend>
          {#each savers as n (n)}
            <label class="check"><input type="checkbox" value={n} bind:group={homeSet} /> {n}</label>
          {:else}
            <small>No <code>erh:vmodutils:arm-position-saver</code> switches found.</small>
          {/each}
        </fieldset>
      </details>
```

Add the styles:

```css
  .homing { position: absolute; inset: 0; display: grid; place-items: center; background: rgba(0, 0, 0, 0.45); color: #fff; font-weight: 600; }
  .setup { font-size: 13px; }
  .setup summary { cursor: pointer; color: var(--muted); }
  .setup fieldset { border: 0; margin: 0; padding: 8px 0 0; display: grid; gap: 4px; }
  .setup legend { color: var(--muted); padding: 0 0 4px; }
  label.check { display: flex; flex: none; gap: 6px; align-items: center; color: inherit; }
  label.check input { width: auto; }
```

The existing rule `label { display: grid; flex: 1 1 110px; … }` also matches these labels. `label.check` overrides it. Check that visually.

- [ ] **Step 4: Check, test, build**

Run: `npm run check && npm test && npm run build`
Expected: 0 errors, all tests pass, and the build succeeds.

- [ ] **Step 5: Commit**

```bash
git add src/Recorder.svelte
git commit -m "feat(ui): home via arm-position-saver switches, robot setup, H key"
```

---

### Task 6: Shortcut overlay, hint line, README

**Files:**
- Modify: `src/Recorder.svelte`, `README.md`

- [ ] **Step 1: Overlay state, and the final `onKey`**

Add `let showKeys = $state(false);`. Then replace `onKey` with this final version, which merges Tasks 2, 4 and 5 and adds `?` and overlay Esc:

```ts
  function onKey(e: KeyboardEvent) {
    if (e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target instanceof Element && e.target.closest('input, select, textarea')) return;
    if (e.key === '?') {
      showKeys = !showKeys;
    } else if (e.key === 'Escape' && showKeys) {
      showKeys = false;
    } else if (/^[1-9]$/.test(e.key)) {
      selectTask(Number(e.key) - 1);
    } else if (e.key === 't' || e.key === 'T') {
      if (teleop && !homing) teleopRequested = true;
    } else if (e.key === 'h' || e.key === 'H') {
      if (canHome) homeRequested = true;
    } else if (e.key === 'Enter' && !active && !(e.target instanceof Element && e.target.closest('button, summary, a'))) {
      formEl?.requestSubmit();
    } else if (active && e.code === 'Space') {
      e.preventDefault();
      advanceRequested = true;
    } else if (active && e.key === 'Escape') {
      stopRequested = true;
    }
  }
```

- [ ] **Step 2: Markup**

In the active branch, replace the `<small>space: …</small>` hint with:

```svelte
        <small class="hint">
          <kbd>space</kbd> {session.phase === 'recording' ? 'end episode' : 'start next episode'} · <kbd>esc</kbd> stop session ·
          <kbd>1–9</kbd> task · <kbd>T</kbd> teleop · <kbd>H</kbd> home · <kbd>?</kbd> shortcuts
        </small>
```

As the last child of `<main>`, add:

```svelte
  {#if showKeys}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div class="overlay" role="dialog" aria-label="Keyboard shortcuts" tabindex="-1" onclick={(e) => e.target === e.currentTarget && (showKeys = false)}>
      <table class="keys">
        <tbody>
          <tr><td>Space</td><td>End episode / start next episode</td></tr>
          <tr><td>Esc</td><td>Stop session</td></tr>
          <tr><td>1–9</td><td>Pick task (queued while recording)</td></tr>
          <tr><td>T</td><td>Toggle teleop. While recording: stop teleop and end the episode.</td></tr>
          <tr><td>H</td><td>Home arms; teleop stays off (not while recording)</td></tr>
          <tr><td>Enter</td><td>Start session</td></tr>
          <tr><td>?</td><td>Show / hide shortcuts</td></tr>
        </tbody>
      </table>
    </div>
  {/if}
```

Add `position: relative;` to the **existing** `main { … }` rule. Don't add a second `main` rule. Then add:

```css
  .overlay { position: absolute; inset: 0; display: grid; place-items: center; background: rgba(0, 0, 0, 0.55); padding: 16px; }
  .keys { background: var(--panel); border-radius: 8px; padding: 12px 16px; border-collapse: collapse; }
  .keys td { padding: 4px 8px; border-top: 1px solid var(--border); }
  .keys td:first-child { font-weight: 600; white-space: nowrap; }
  .hint kbd { border: 1px solid var(--border); border-radius: 3px; padding: 0 4px; font-size: 11px; }
```

- [ ] **Step 3: README**

In `README.md`, after the paragraph that starts "Each episode becomes one Viam sequence…", add:

````markdown
3. Optional, for teleop and home resets:
   - A `devrel:so101:teleop` generic service. The app uses it automatically if there's exactly one. Keep `manage_leader_torque` at its default (`true`).
   - One `erh:vmodutils:arm-position-saver` switch per arm, for example `leader-home` and `follower-home`. Choose the home set under **Robot setup**.

**H** stops teleop and moves each arm in the home set to its saved pose. Teleop stays off afterwards; press **T** once your hand is on the leader. While teleop is off, a reset doesn't start the next episode on its own; it waits for **Space**.

### Keyboard

| Key | Action |
|---|---|
| Space | End episode / start next episode |
| Esc | Stop session |
| 1–9 | Pick task for the next episode |
| T | Toggle teleop (while recording: stop teleop and end the episode) |
| H | Home arms (not while recording) |
| Enter | Start session |
| ? | Shortcut overlay |

Tasks are saved in the browser. Each episode is tagged `task:<the task active when it started>`.
````

- [ ] **Step 4: Check, test, build**

Run: `npm run check && npm test && npm run build`
Expected: 0 errors, all tests pass, and the build succeeds.

- [ ] **Step 5: Commit**

```bash
git add src/Recorder.svelte README.md
git commit -m "feat(ui): shortcut overlay and hint line; docs for tasks, teleop, home"
```

---

### Task 7: End-to-end on pi-arm6 (operator, by hand)

- [ ] **Step 1:** Make sure pi-arm6 has `leader-home` and `follower-home` arm-position-saver switches. If they're missing, add them. Then, for each one, put the arm at home and call `SetPosition(1)` to save the pose.
- [ ] **Step 2:** Deploy the build: run `npm run build` and use the local server, or upload a module version.
- [ ] **Step 3:** In Robot setup, check both savers.
- [ ] **Step 4:** Add the tasks `reach`, `grasp` and `place`. Start a 4-episode session with an open-ended episode length.
  1. During episode 1 (`reach`), press 2. The `grasp` chip shows "next".
  2. In reset, press H. Teleop stops, both arms go home, and the reset shows "held". Press T with your hand on the leader, then Space. Episode 2 records as `grasp`.
  3. During episode 3, press T. Teleop stops, the episode ends, and the reset is held. Press T, then Space.
- [ ] **Step 5:** Check Viam Data. There should be 4 sequences with the `task:` tags you picked, and the chip counts should match.
- [ ] **Step 6:** Run a ponytail audit (`/ponytail-audit`) before merging, per the project workflow.
