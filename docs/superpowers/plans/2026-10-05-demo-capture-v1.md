# Demo Capture v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a LeLab-style recording-session web app. Each episode is one Viam sequence, tagged `task:` / `session:` / `fps:`, and the app shows live camera previews.

**Architecture:**
- **Frontend only.** A Svelte 5 + Vite app builds into `dist/`. The existing Go module (`module.go`) embeds `dist/` and serves it unchanged.
- **Capture is driven by `DoCommand`** on a `viam:capture-control:capture-control-sensor` already configured on the machine. The data manager polls that sensor at 10 Hz and turns `start_capture` / `stop_capture` into collectors plus sequences.
- **The session engine is a pure TS module.** It has no Svelte or SDK imports, and `node:test` tests it directly.

**Tech Stack:**
- Svelte 5, Vite 8, `@viamrobotics/svelte-sdk` 1.x, and `@viamrobotics/sdk` 0.76 (already installed).
- `node:test` on Node 24, which runs `.ts` natively by stripping types.

**Spec:** `docs/superpowers/specs/2026-10-05-demo-capture-v1-design.md`

---

## File map

| File | Responsibility |
|---|---|
| `package.json` | Modified: deps plus `dev` / `build` / `check` / `test` scripts. |
| `index.html`, `vite.config.ts`, `tsconfig.json`, `src/main.ts` | Vite + Svelte entry, with relative asset paths (`base: './'`). |
| `src/auth.js` | Moved from the repo root with `git mv`, content unchanged. Reads Viam app credential cookies. |
| `src/capture.ts` | Session engine (start/tick/next/stop to commands) and readings helpers for the controller. |
| `src/capture.test.ts` | `node:test` tests for `capture.ts`. |
| `src/App.svelte` | Reads credentials and wraps the app in `ViamProvider`. |
| `src/Recorder.svelte` | Controller discovery, setup form, status panel, camera grid, keys, command driver, orphan banner. |
| `Makefile` | Builds the frontend before the Go binary and runs `npm test`. |
| `README.md` | Machine setup and dev loop. |

Facts the implementer needs (verified against the RDK and the sensor source):
- **DoCommand.**
  - `start_capture` takes `{"start_capture": true, "frequency_hz": <n>, "tags": [...]}`. It sets the frequency and tags on every controlled resource and opens a sequence with the same tags.
  - `stop_capture` takes `{"stop_capture": true}`. It sets the frequency to 0 and closes the sequence.
  - Always send `frequency_hz`: the sensor's default is 0, which means no capture.
- **Readings.** The controller's `Readings()` looks like `{"overrides": [{resource_name, method, capture_frequency_hz, tags?}...], "sequences": [] | [{resources, sequence_tags}]}`. `sequences` is non-empty exactly while a sequence is open.
- **Retries are safe.** Sending `start_capture` again with identical tags does not open a second sequence, because the data manager keys open sequences by resources plus tags.

---

### Task 1: Vite + Svelte scaffold building into `dist/`

**Files:**
- Modify: `package.json`, `Makefile`
- Create: `index.html`, `vite.config.ts`, `tsconfig.json`, `src/main.ts`, `src/App.svelte` (placeholder)
- Move: `auth.js` → `src/auth.js`

- [ ] **Step 1: Install dependencies and set scripts**

```bash
npm install @viamrobotics/svelte-sdk
npm install -D svelte vite @sveltejs/vite-plugin-svelte svelte-check typescript
npm pkg set private=true --json && npm pkg set type=module
npm pkg set scripts.dev="vite" scripts.build="vite build" scripts.check="svelte-check" scripts.test="node --test src/capture.test.ts"
```

`@tanstack/svelte-query` is a regular dependency of the svelte-sdk, so don't install it separately.

- [ ] **Step 2: Move auth.js**

```bash
git mv auth.js src/auth.js
```

- [ ] **Step 3: Create `vite.config.ts`**

```ts
import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// base './' — Viam Apps and the local module server need relative asset paths.
export default defineConfig({ base: './', plugins: [svelte()] });
```

- [ ] **Step 4: Create `tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "noEmit": true,
    "allowImportingTsExtensions": true,
    "allowJs": true,
    "verbatimModuleSyntax": true,
    "skipLibCheck": true
  },
  "include": ["src"],
  "exclude": ["src/**/*.test.ts"]
}
```

- [ ] **Step 5: Create `index.html`**

```html
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <title>Demo Capture</title>
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

- [ ] **Step 6: Create `src/main.ts` and a placeholder `src/App.svelte`**

```ts
import { mount } from 'svelte';
import App from './App.svelte';

mount(App, { target: document.getElementById('app')! });
```

```svelte
<p>Demo Capture</p>
```

- [ ] **Step 7: Wire the Makefile**

In `Makefile`, add this rule above the `$(MODULE_BINARY)` rule:

```make
$(ENTRYPOINT): package.json package-lock.json index.html vite.config.ts $(wildcard src/*)
	npm run build
```

Change `test:` so that `go:embed dist/**` has something to embed, and so the JS tests run too:

```make
test: $(ENTRYPOINT)
	go test ./...
	npm test
```

Append `npm ci` to the `setup:` recipe:

```make
setup:
	go get github.com/erh/vmodutils@latest
	go mod tidy
	npm ci
```

- [ ] **Step 8: Verify the build output uses relative paths**

Run: `npm run build && grep -o 'src="[^"]*"' dist/index.html`
Expected: `src="./assets/index-<hash>.js"`. It must start with `./`, not `/`.

- [ ] **Step 9: Verify the Go module still builds against the new `dist/`**

Run: `go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 10: Commit**

```bash
git add package.json package-lock.json index.html vite.config.ts tsconfig.json src Makefile
git commit -m "feat: scaffold Svelte + Vite frontend building into dist/"
```

---

### Task 2: Session engine (`src/capture.ts`), TDD

**Files:**
- Create: `src/capture.test.ts`, `src/capture.ts`

- [ ] **Step 1: Write the failing tests in `src/capture.test.ts`**

```ts
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cap from './capture.ts';

const settings: cap.Settings = { task: 'pick cube', episodes: 2, episodeS: 10, resetS: 5, fps: 15 };
const START = { start_capture: true, frequency_hz: 15, tags: ['task:pick cube', 'session:abc', 'fps:15'] };
const STOP = { stop_capture: true };

test('full run: record, reset, record, done; no reset after the last episode', () => {
  let [s, cmds] = cap.start(settings, 'abc', 0);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.phase, 'recording');
  assert.equal(s.episode, 1);

  [s, cmds] = cap.tick(s, 9_999);
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'recording');

  [s, cmds] = cap.tick(s, 10_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'reset');

  [s, cmds] = cap.tick(s, 15_000);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.phase, 'recording');
  assert.equal(s.episode, 2);

  [s, cmds] = cap.tick(s, 25_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'done');

  [s, cmds] = cap.tick(s, 99_000);
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'done');
});

test('next() ends recording and reset early, restarting the timer from now', () => {
  let [s] = cap.start(settings, 'abc', 0);
  let cmds: cap.Command[];

  [s, cmds] = cap.next(s, 3_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'reset');
  assert.equal(cap.remainingMs(s, 3_000), 5_000);

  [s, cmds] = cap.next(s, 4_000);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.episode, 2);
  assert.equal(cap.remainingMs(s, 4_000), 10_000);
});

test('stop() while recording sends stop_capture; during reset it sends nothing', () => {
  let [s] = cap.start(settings, 'abc', 0);
  let [stopped, cmds] = cap.stop(s);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(stopped.phase, 'done');

  [s] = cap.next(s, 1_000); // now in reset
  [stopped, cmds] = cap.stop(s);
  assert.deepEqual(cmds, []);
  assert.equal(stopped.phase, 'done');
});

test('controller readings helpers', () => {
  const r = {
    overrides: [
      { resource_name: 'arm', method: 'JointPositions', capture_frequency_hz: 0 },
      { resource_name: 'wrist-cam', method: 'GetImages', capture_frequency_hz: 0 },
    ],
    sequences: [],
  };
  assert.equal(cap.isController(r), true);
  assert.equal(cap.isController({ temperature: 20 }), false);
  assert.deepEqual(cap.controlledResources(r), ['arm', 'wrist-cam']);
  assert.equal(cap.hasOpenSequence(r), false);
  assert.equal(cap.hasOpenSequence({ ...r, sequences: [{ resources: [], sequence_tags: [] }] }), true);
});
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `npm test`
Expected: FAIL with `ERR_MODULE_NOT_FOUND` for `./capture.ts`.

- [ ] **Step 3: Implement `src/capture.ts`**

Use only erasable TS syntax (no `enum`, no parameter properties), so that Node's type stripping can run it.

```ts
// Recording-session engine. Pure: no Svelte or SDK imports. Each transition returns the
// next session plus the DoCommands to send to the capture-control-sensor, in order.

export type Settings = { task: string; episodes: number; episodeS: number; resetS: number; fps: number };
export type Phase = 'recording' | 'reset' | 'done';
export type Session = { id: string; settings: Settings; phase: Phase; episode: number; endsAt: number };
export type Command =
  | { start_capture: true; frequency_hz: number; tags: string[] }
  | { stop_capture: true };
export type Step = [Session, Command[]];

const STOP: Command = { stop_capture: true };

export function tags(settings: Settings, id: string): string[] {
  return [`task:${settings.task}`, `session:${id}`, `fps:${settings.fps}`];
}

// frequency_hz must always be sent: the sensor's default_capture_frequency_hz is 0 (= no capture).
function startCapture(s: Session): Command {
  return { start_capture: true, frequency_hz: s.settings.fps, tags: tags(s.settings, s.id) };
}

export function start(settings: Settings, id: string, now: number): Step {
  const s: Session = { id, settings, phase: 'recording', episode: 1, endsAt: now + settings.episodeS * 1000 };
  return [s, [startCapture(s)]];
}

/** Ends the current phase now (space bar). The last episode skips reset and goes to done. */
export function next(s: Session, now: number): Step {
  if (s.phase === 'recording') {
    if (s.episode >= s.settings.episodes) return [{ ...s, phase: 'done' }, [STOP]];
    return [{ ...s, phase: 'reset', endsAt: now + s.settings.resetS * 1000 }, [STOP]];
  }
  if (s.phase === 'reset') {
    const r: Session = { ...s, phase: 'recording', episode: s.episode + 1, endsAt: now + s.settings.episodeS * 1000 };
    return [r, [startCapture(r)]];
  }
  return [s, []];
}

export function tick(s: Session, now: number): Step {
  return now >= s.endsAt ? next(s, now) : [s, []];
}

/** Stops the session. Capture is already stopped during reset, so only recording needs stop_capture. */
export function stop(s: Session): Step {
  return [{ ...s, phase: 'done' }, s.phase === 'recording' ? [STOP] : []];
}

export function remainingMs(s: Session, now: number): number {
  return s.phase === 'done' ? 0 : Math.max(0, s.endsAt - now);
}

// Helpers for reading viam:capture-control:capture-control-sensor Readings().
type Readings = Record<string, unknown>;

export const isController = (r: Readings): boolean => Array.isArray(r.overrides) && Array.isArray(r.sequences);

export const controlledResources = (r: Readings): string[] =>
  (r.overrides as { resource_name: string }[]).map((o) => o.resource_name);

export const hasOpenSequence = (r: Readings): boolean => (r.sequences as unknown[]).length > 0;
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `npm test`
Expected: `# pass 4`, `# fail 0`.

- [ ] **Step 5: Commit**

```bash
git add src/capture.ts src/capture.test.ts
git commit -m "feat: add recording session engine with tests"
```

---

### Task 3: App shell and Recorder UI

**Files:**
- Modify: `src/App.svelte` (replace the placeholder)
- Create: `src/Recorder.svelte`

- [ ] **Step 1: Write `src/App.svelte`**

`dialConfigs` keys are arbitrary labels; the SDK only uses them to look up the connection. We have one machine, so the key is `main`.

```svelte
<script lang="ts">
  import type { DialConf } from '@viamrobotics/sdk';
  import { ViamProvider } from '@viamrobotics/svelte-sdk';
  import { getHostAndCredentials } from './auth.js';
  import Recorder from './Recorder.svelte';

  const { host, credentials } = getHostAndCredentials();
  const dialConfigs: Record<string, DialConf> = {
    main: { host, credentials, signalingAddress: 'https://app.viam.com:443' } as DialConf,
  };
</script>

{#if host}
  <ViamProvider {dialConfigs}>
    <Recorder partID="main" />
  </ViamProvider>
{:else}
  <p>No machine credentials found. Open this app from Viam, or run it locally with <code>viam module local-app-testing</code>.</p>
{/if}

<style>
  :global(:root) {
    --bg: #fafafa; --fg: #1a1a1a; --muted: #666; --panel: #fff; --border: #ddd;
    --rec: #d22; --reset: #c80; --done: #2a7;
    color-scheme: light dark;
  }
  @media (prefers-color-scheme: dark) {
    :global(:root) { --bg: #111; --fg: #eee; --muted: #999; --panel: #1b1b1b; --border: #333; }
  }
  :global(body) { margin: 0; background: var(--bg); color: var(--fg); font: 15px/1.4 system-ui, sans-serif; }
</style>
```

- [ ] **Step 2: Write `src/Recorder.svelte`**

How the command driver works:
- A single 100 ms interval is the only thing that sends commands, so commands can never interleave. Space and Esc only set request flags, which the next tick consumes.
- `apply()` sends a step's commands in order and commits the new session state only after they all succeed.
- If a command fails, the session goes to `done` and `stop_capture` is attempted. Then the orphan check (which re-runs whenever `active` flips) shows the banner if a sequence is still open.

```svelte
<script lang="ts">
  import { onMount } from 'svelte';
  import { MachineConnectionEvent, SensorClient } from '@viamrobotics/sdk';
  import { CameraStream, useConnectionStatus, useResourceNames, useRobotClient } from '@viamrobotics/svelte-sdk';
  import * as cap from './capture.ts';

  let { partID }: { partID: string } = $props();

  const robot = useRobotClient(() => partID);
  const status = useConnectionStatus(() => partID);
  const sensors = useResourceNames(() => partID, 'sensor');
  const cameras = useResourceNames(() => partID, 'camera');

  const DEFAULTS: cap.Settings = { task: '', episodes: 10, episodeS: 20, resetS: 10, fps: 10 };
  function loadSettings(): cap.Settings {
    try {
      return { ...DEFAULTS, ...JSON.parse(localStorage.getItem('settings') ?? '{}') };
    } catch {
      return { ...DEFAULTS };
    }
  }

  let controllers = $state<{ name: string; resources: string[] }[]>([]);
  let controllerName = $state('');
  let settings = $state(loadSettings());
  let session = $state.raw<cap.Session | undefined>();
  let now = $state(Date.now());
  let error = $state('');
  let orphaned = $state(false);

  // The command driver's request flags (see onMount).
  let busy = false;
  let advanceRequested = false;
  let stopRequested = false;

  const connected = $derived(status.current === MachineConnectionEvent.CONNECTED);
  const controller = $derived(controllers.find((c) => c.name === controllerName));
  const sensor = $derived(robot.current && controllerName ? new SensorClient(robot.current, controllerName) : undefined);
  const cameraNames = $derived(cameras.current.map((r) => r.name).filter((n) => controller?.resources.includes(n)));
  const active = $derived(session?.phase === 'recording' || session?.phase === 'reset');

  // Discover capture-control sensors by the shape of their readings.
  $effect(() => {
    const client = robot.current;
    const names = sensors.current.map((r) => r.name);
    if (!client || !connected) return;
    Promise.all(
      names.map(async (name) => {
        try {
          const r = await new SensorClient(client, name).getReadings();
          return cap.isController(r) ? { name, resources: cap.controlledResources(r) } : undefined;
        } catch {
          return undefined;
        }
      }),
    ).then((found) => {
      controllers = found.filter((c) => c !== undefined);
      if (!controllers.some((c) => c.name === controllerName)) {
        controllerName = controllers.length === 1 ? controllers[0].name : '';
      }
    });
  });

  // Orphan check: the sensor has no watchdog, so a closed tab can leave capture running.
  // Re-runs on controller change, reconnect, and whenever a session ends.
  $effect(() => {
    const s = sensor;
    if (!s || !connected || active) return;
    s.getReadings()
      .then((r) => (orphaned = cap.hasOpenSequence(r)))
      .catch(() => {});
  });

  // A lost connection stops the session locally. The orphan check runs on reconnect.
  $effect(() => {
    if (!connected && session && active) {
      error = 'Connection lost — session stopped.';
      session = cap.stop(session)[0];
    }
  });

  async function send(cmd: cap.Command) {
    if (!sensor) throw new Error('No capture controller selected');
    let last: unknown;
    for (let i = 0; i < 3; i++) {
      try {
        return await sensor.doCommand(cmd);
      } catch (e) {
        last = e;
        await new Promise((r) => setTimeout(r, 300));
      }
    }
    throw last;
  }

  async function apply([nextSession, cmds]: cap.Step) {
    if (cmds.length === 0) {
      session = nextSession;
      return;
    }
    busy = true;
    try {
      for (const c of cmds) await send(c);
      session = nextSession;
    } catch (e) {
      error = `Capture command failed: ${e instanceof Error ? e.message : e}`;
      session = cap.stop(nextSession)[0];
      await send({ stop_capture: true }).catch(() => {});
    } finally {
      busy = false;
    }
  }

  function startSession() {
    error = '';
    advanceRequested = stopRequested = false; // drop keys pressed during a previous session
    const snapshot = $state.snapshot(settings);
    try {
      localStorage.setItem('settings', JSON.stringify(snapshot));
    } catch {}
    apply(cap.start(snapshot, crypto.randomUUID().slice(0, 8), Date.now()));
  }

  function stopOrphan() {
    send({ stop_capture: true })
      .then(() => (orphaned = false))
      .catch((e) => (error = `Stop failed: ${e instanceof Error ? e.message : e}`));
  }

  function onKey(e: KeyboardEvent) {
    if (!active) return;
    if (e.code === 'Space') {
      e.preventDefault();
      advanceRequested = true;
    } else if (e.key === 'Escape') {
      stopRequested = true;
    }
  }

  onMount(() => {
    // The only place that sends session commands, so they never interleave.
    const timer = setInterval(async () => {
      now = Date.now();
      if (!session || !active || busy) return;
      if (stopRequested) {
        stopRequested = advanceRequested = false;
        await apply(cap.stop(session));
      } else if (advanceRequested) {
        advanceRequested = false;
        await apply(cap.next(session, now));
      } else {
        await apply(cap.tick(session, now));
      }
    }, 100);

    // Best effort: a closing tab can't await the result.
    const onHide = () => {
      if (session?.phase === 'recording') sensor?.doCommand({ stop_capture: true });
    };
    addEventListener('pagehide', onHide);
    return () => {
      clearInterval(timer);
      removeEventListener('pagehide', onHide);
    };
  });

  const LABELS = { recording: '● RECORDING', reset: '◐ RESET', done: 'DONE' };
</script>

<svelte:window onkeydown={onKey} />

<main>
  <section class="side">
    {#if error}<p class="banner error" role="alert">{error}</p>{/if}
    {#if orphaned}
      <p class="banner warn" role="alert">
        Capture is still running on <strong>{controllerName}</strong>.
        <button onclick={stopOrphan}>Stop</button>
      </p>
    {/if}

    {#if !connected}
      <p>Connecting to machine…</p>
    {:else if controllers.length === 0}
      <p>
        No capture controller found. Add a <code>viam:capture-control:capture-control-sensor</code> and set the data
        manager's <code>capture_control_sensor</code> to <code>{'{"name": "<sensor>", "key": "overrides"}'}</code>.
      </p>
    {:else if !active}
      <form
        onsubmit={(e) => {
          e.preventDefault();
          startSession();
        }}
      >
        <label>Controller
          <select bind:value={controllerName} required>
            {#each controllers as c (c.name)}<option>{c.name}</option>{/each}
          </select>
        </label>
        <label>Task <input bind:value={settings.task} required placeholder="pick up the red cube" /></label>
        <div class="row">
          <label>Episodes <input type="number" min="1" step="1" required bind:value={settings.episodes} /></label>
          <label>FPS <input type="number" min="0.1" step="any" required bind:value={settings.fps} /></label>
        </div>
        <div class="row">
          <label>Episode (s) <input type="number" min="1" required bind:value={settings.episodeS} /></label>
          <label>Reset (s) <input type="number" min="0" required bind:value={settings.resetS} /></label>
        </div>
        <button type="submit" disabled={!sensor || orphaned}>Start session</button>
      </form>
    {/if}

    {#if session}
      <div class="status {session.phase}" aria-live="polite">
        <strong>{LABELS[session.phase]}</strong>
        {#if active}<div class="clock">{Math.ceil(cap.remainingMs(session, now) / 1000)}s</div>{/if}
        <div>Episode {session.episode} / {session.settings.episodes} · task:{session.settings.task}</div>
        {#if active}<small>space: next phase · esc: stop</small>{/if}
      </div>
    {/if}
  </section>

  <section class="cams">
    {#each cameraNames as name (name)}
      <figure>
        <CameraStream {partID} {name} />
        <figcaption>{name}</figcaption>
      </figure>
    {:else}
      <p class="muted">Cameras in the controller's resources appear here.</p>
    {/each}
  </section>
</main>

<style>
  main { display: grid; grid-template-columns: minmax(280px, 360px) 1fr; gap: 16px; padding: 16px; }
  @media (max-width: 720px) { main { grid-template-columns: 1fr; } }
  form, .status { display: grid; gap: 10px; background: var(--panel); border: 1px solid var(--border); border-radius: 8px; padding: 12px; }
  label { display: grid; gap: 4px; font-size: 13px; color: var(--muted); }
  input, select, button { font: inherit; padding: 6px 8px; }
  .row { display: grid; grid-template-columns: 1fr 1fr; gap: 8px; }
  .status { margin-top: 12px; text-align: center; }
  .status strong { font-size: 24px; }
  .status.recording strong { color: var(--rec); }
  .status.reset strong { color: var(--reset); }
  .status.done strong { color: var(--done); }
  .clock { font-size: 48px; font-variant-numeric: tabular-nums; }
  .banner { padding: 8px 12px; border-radius: 6px; border: 1px solid; }
  .error { border-color: var(--rec); }
  .warn { border-color: var(--reset); }
  .cams { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 8px; align-content: start; }
  figure { margin: 0; }
  figure :global(video) { width: 100%; background: #000; border-radius: 6px; }
  figcaption, .muted { color: var(--muted); font-size: 13px; }
</style>
```

- [ ] **Step 3: Type-check and build**

Run: `npm run check && npm run build`
Expected: `svelte-check found 0 errors`, then the Vite build succeeds.
If `svelte-check` flags the `credentials` type from `auth.js`, the `as DialConf` cast in `App.svelte` is the intended fix. Don't modify `auth.js`.

- [ ] **Step 4: Commit**

```bash
git add src/App.svelte src/Recorder.svelte
git commit -m "feat: add recorder UI with controller discovery, session driver, camera previews"
```

---

### Task 4: README, then end-to-end check on a real machine

**Files:**
- Modify: `README.md` (replace the scaffold's "Build your frontend" section with the content below; keep the title and the docs link)

- [ ] **Step 1: Write the README sections**

````markdown
## Machine setup

1. Add the capture controller sensor (module `viam:capture-control`) and list the resources to record:

```json
{
  "name": "demo-capture",
  "api": "rdk:component:sensor",
  "model": "viam:capture-control:capture-control-sensor",
  "attributes": {
    "resources": [
      { "resource_name": "arm", "method": "JointPositions" },
      { "resource_name": "wrist-cam", "method": "GetImages" }
    ]
  }
}
```

2. Point the data manager at it, and add the sensor to the data manager's `depends_on`:

```json
"attributes": { "capture_control_sensor": { "name": "demo-capture", "key": "overrides" } }
```

Each episode becomes one Viam sequence tagged `task:<task>`, `session:<id>`, `fps:<fps>`. Nothing is captured between episodes.

## Develop

```bash
make setup     # go deps + npm ci
npm run dev    # Vite dev server
viam module local-app-testing --app-url http://localhost:5173 --machine-id <machine-id>  # injects credential cookies; confirm flags with --help
npm test       # session engine tests
make           # go test + npm test + module.tar.gz
```
````

- [ ] **Step 2: Run the full local build**

Run: `make`
Expected: `go test` passes, `npm test` reports `# pass 4`, and `module.tar.gz` is created.

- [ ] **Step 3: Manual end-to-end check (needs a real machine and a human)**

On a machine configured as described in the README:

1. Open the app. The controller is auto-selected, and the camera previews show the controller's cameras.
2. Run a session with 2 episodes, 5 s episodes, 3 s reset, and 5 fps. Check that the status goes RECORDING → RESET → RECORDING → DONE, and that there's no reset after episode 2.
3. Press space mid-episode and check that it jumps to reset. Press Esc during recording and check that it stops.
4. Start an episode, then close the tab. Reopen the app. Either `pagehide` already stopped capture, or the "Capture is still running" banner appears and **Stop** clears it.
5. In Viam Data, check that the sequences carry the tags `task:…`, `session:<id>`, and `fps:5`, and that their time windows match the episodes.

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "docs: machine setup and dev loop for demo capture app"
```

Known risk: the cloud build (`meta.json` → `make setup` / `make module.tar.gz`) now needs Node and npm on the build runner. Check this on the first `viam module build start`. If Node isn't there, build `dist/` in CI before the module build instead.
