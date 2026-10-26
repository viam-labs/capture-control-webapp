# Teleop Feedback Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Address feedback from the first test on a real robot:
- Open-ended "Episode length (s)".
- Fix the broken form layout.
- A camera-first layout (option B: full-width cameras, controls in a bottom drawer).
- Camera streams that survive reconnects.

**Architecture:** The changes are in the frontend only.
- `src/capture.ts` gets nullable episode length and phase timing (`startedAt`, plus `endsAt = Infinity` for open-ended episodes).
- A new `src/CameraFeed.svelte` replaces the svelte-sdk `<CameraStream>`.
- `src/Recorder.svelte` is re-laid out.

**Tech Stack:** Svelte 5, `@viamrobotics/sdk` 0.76 `StreamClient`, and `node:test`.

**Decisions (confirmed by the user):**
- Use layout B.
- Show only the cameras the capture controller records.
- An empty episode length means the episode runs until Space.

**Why the svelte-sdk `<CameraStream>` loses video on reconnect:** This is from reading `create-stream-client.svelte.js` in svelte-sdk 1.5.3 and `StreamClient` in the TS SDK.
1. On DISCONNECTED, the svelte-sdk resets its queries. The camera list goes to `[]` and `<CameraStream>` unmounts.
2. Its cleanup calls `StreamClient.remove()`, which fails while offline. The old `StreamClient` therefore keeps the name in its `streams` set, and it is still subscribed to the robot client's CONNECTED event.
3. On reconnect, that orphaned `StreamClient` re-adds the stream. The remounted component's `getStream()` then adds it again, but no new `track` event arrives, so `getStream()` rejects after 5 s.
4. Nothing retries, so the video stays black.

**Fix:**
- Keep one long-lived `StreamClient` per `RobotClient`. Listen to `track` events the whole time, and swap in any track whose stream id matches the camera.
- Run a 1 s watchdog: if the machine is connected and the camera has no live video track (and nothing was requested in the last 5 s), call `remove`, then `add`, to force a fresh track.
- Keep the camera list stable while the resource-names query is reset.

---

### Task 1: Open-ended episode length in the session engine (TDD)

**Files:**
- Modify: `src/capture.ts`
- Test: `src/capture.test.ts`

- [ ] **Step 1: Add the failing test**

Add this at the end of `src/capture.test.ts`:

```ts
test('open-ended episode (episodeS null) runs until next(); reset stays timed', () => {
  const open: cap.Settings = { ...settings, episodeS: null };
  let [s] = cap.start(open, 'abc', 0);
  let cmds: cap.Command[];

  [s, cmds] = cap.tick(s, 1e9);
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'recording');
  assert.equal(cap.remainingMs(s, 1e9), Infinity);
  assert.equal(cap.elapsedMs(s, 7_000), 7_000);

  [s, cmds] = cap.next(s, 8_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'reset');
  assert.equal(cap.remainingMs(s, 8_000), 5_000);
  assert.equal(cap.elapsedMs(s, 9_000), 1_000);

  [s, cmds] = cap.tick(s, 13_000);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.phase, 'recording');
  assert.equal(s.episode, 2);
  assert.equal(cap.remainingMs(s, 13_000), Infinity);
});
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `npm test`
Expected: the new test fails, because `cap.elapsedMs is not a function`, or a tick at 1e9 moves to reset since `endsAt` is `NaN`. The 4 existing tests still pass.

- [ ] **Step 3: Implement it in `src/capture.ts`**

Change the `Settings` and `Session` types:

```ts
export type Settings = { task: string; episodes: number; episodeS: number | null; resetS: number; fps: number };
export type Phase = 'recording' | 'reset' | 'done';
// startedAt/endsAt bound the current phase; endsAt is Infinity for an open-ended episode.
export type Session = { id: string; settings: Settings; phase: Phase; episode: number; startedAt: number; endsAt: number };
```

Add this helper above `start`:

```ts
// null seconds = open-ended: the phase only ends via next() (space bar).
const until = (now: number, seconds: number | null): number => (seconds == null ? Infinity : now + seconds * 1000);
```

Replace `start` and `next`:

```ts
export function start(settings: Settings, id: string, now: number): Step {
  const s: Session = { id, settings, phase: 'recording', episode: 1, startedAt: now, endsAt: until(now, settings.episodeS) };
  return [s, [startCapture(s)]];
}

/** Ends the current phase now (space bar). The last episode skips reset and goes to done. */
export function next(s: Session, now: number): Step {
  if (s.phase === 'recording') {
    if (s.episode >= s.settings.episodes) return [{ ...s, phase: 'done' }, [STOP]];
    return [{ ...s, phase: 'reset', startedAt: now, endsAt: until(now, s.settings.resetS) }, [STOP]];
  }
  if (s.phase === 'reset') {
    const r: Session = {
      ...s,
      phase: 'recording',
      episode: s.episode + 1,
      startedAt: now,
      endsAt: until(now, s.settings.episodeS),
    };
    return [r, [startCapture(r)]];
  }
  return [s, []];
}
```

Add this below `remainingMs`:

```ts
export function elapsedMs(s: Session, now: number): number {
  return Math.max(0, now - s.startedAt);
}
```

Don't change `tick`, `stop` or `remainingMs`: `now >= Infinity` is false, and `Infinity - now` is `Infinity`.

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `npm test && npm run check`
Expected: `# pass 5`, `# fail 0`, and svelte-check reports 0 errors. If `Recorder.svelte` type errors appear because of the nullable `episodeS`, leave them for Task 2. Report them, but don't fix them here.

- [ ] **Step 5: Commit**

```bash
git add src/capture.ts src/capture.test.ts
git commit -m "feat: support open-ended episode length in session engine"
```

---

### Task 2: CameraFeed component and the camera-first layout (B)

**Files:**
- Create: `src/CameraFeed.svelte`
- Modify: `src/Recorder.svelte`

- [ ] **Step 1: Create `src/CameraFeed.svelte`**

```svelte
<script module lang="ts">
  import { StreamClient, type RobotClient } from '@viamrobotics/sdk';

  // One StreamClient per RobotClient for the app's lifetime. StreamClient re-adds its
  // streams on every CONNECTED event, and a single instance avoids the orphaned-client
  // race that leaves the svelte-sdk's <CameraStream> black after a reconnect.
  const streamClients = new WeakMap<RobotClient, StreamClient>();
  function streamClientFor(client: RobotClient): StreamClient {
    let sc = streamClients.get(client);
    if (!sc) {
      sc = new StreamClient(client);
      streamClients.set(client, sc);
    }
    return sc;
  }
</script>

<script lang="ts">
  import { MachineConnectionEvent } from '@viamrobotics/sdk';
  import { useConnectionStatus, useRobotClient } from '@viamrobotics/svelte-sdk';

  let { partID, name }: { partID: string; name: string } = $props();

  const robot = useRobotClient(() => partID);
  const status = useConnectionStatus(() => partID);
  let video = $state<HTMLVideoElement>();
  let live = $state(false);

  $effect(() => {
    const client = robot.current;
    const el = video;
    if (!client || !el) return;
    const streams = streamClientFor(client);
    // Stream ids use '+' in place of ':' for remote resources.
    const ids = [name, name.replaceAll(':', '+')];
    let lastRequest = 0;

    // Listen for the whole lifetime of the component, not once like getStream(): every
    // renegotiation (including after a reconnect) delivers a new track.
    const onTrack = (e: RTCTrackEvent) => {
      const [stream] = e.streams;
      if (stream && ids.includes(stream.id)) el.srcObject = stream;
    };
    streams.on('track', onTrack);

    // Watchdog: while connected without a live video track, re-request the stream.
    // remove() first so the server renegotiates a fresh track even if it thinks the
    // stream is already added.
    const check = () => {
      const stream = el.srcObject as MediaStream | null;
      live = !!stream?.getVideoTracks().some((t) => t.readyState === 'live');
      if (live || status.current !== MachineConnectionEvent.CONNECTED) return;
      if (Date.now() - lastRequest < 5000) return;
      lastRequest = Date.now();
      streams
        .remove(name)
        .catch(() => {})
        .then(() => streams.add(name))
        .catch(() => {});
    };
    check();
    const timer = setInterval(check, 1000);

    return () => {
      clearInterval(timer);
      streams.off('track', onTrack);
      streams.remove(name).catch(() => {});
      el.srcObject = null;
    };
  });
</script>

<figure>
  <video bind:this={video} muted autoplay playsinline></video>
  <figcaption>{name}{#if !live} · connecting…{/if}</figcaption>
</figure>

<style>
  figure { position: relative; margin: 0; min-height: 0; display: grid; }
  video { width: 100%; height: 100%; min-height: 0; object-fit: contain; background: #000; }
  figcaption {
    position: absolute; left: 8px; bottom: 8px; padding: 2px 6px; border-radius: 4px;
    background: rgba(0, 0, 0, 0.55); color: #fff; font-size: 13px;
  }
</style>
```

- [ ] **Step 2: Update the `<script>` of `src/Recorder.svelte`**

Make these exact edits:

1. In the import from `@viamrobotics/svelte-sdk`, remove `CameraStream`. Add `import CameraFeed from './CameraFeed.svelte';` below the `capture.ts` import.
2. Change `DEFAULTS` so the episode length defaults to open-ended:
   ```ts
   const DEFAULTS: cap.Settings = { task: '', episodes: 10, episodeS: null, resetS: 10, fps: 10 };
   ```
3. Replace the `cameraNames` `$derived` line with a list that stays stable while the query is reset on disconnect, so video elements aren't torn down. Also add a column count:
   ```ts
   // The svelte-sdk resets queries on disconnect (cameras.current → []); keep the last
   // known list so CameraFeed stays mounted and can recover its stream on reconnect.
   let cameraNames = $state<string[]>([]);
   $effect(() => {
     if (!cameras.query?.isSuccess) return;
     cameraNames = cameras.current.map((r) => r.name).filter((n) => controller?.resources.includes(n) ?? false);
   });
   const cols = $derived(cameraNames.length <= 1 ? 1 : cameraNames.length <= 4 ? 2 : 3);
   ```
4. Add a clock that counts down when the phase is timed and shows elapsed time when it's open-ended. Put it next to `LABELS`:
   ```ts
   const fmt = (ms: number) => {
     const t = Math.floor(ms / 1000);
     return `${Math.floor(t / 60)}:${String(t % 60).padStart(2, '0')}`;
   };
   const clock = $derived.by(() => {
     if (!session) return '';
     const left = cap.remainingMs(session, now);
     return Number.isFinite(left) ? fmt(left + 999) : `${fmt(cap.elapsedMs(session, now))} elapsed`;
   });
   ```
   `fmt(left + 999)` rounds up, so a countdown never shows `0:00` while time remains.

Leave everything else in the script unchanged: the driver, `apply`, `send`, the effects, keys and `pagehide`.

- [ ] **Step 3: Replace the markup and `<style>` of `src/Recorder.svelte`**

Replace everything from `<main>` through the end of the file. Keep the `<svelte:window onkeydown={onKey} />` line above `<main>`.

```svelte
<main>
  <section class="cams {active ? session?.phase : ''}" style="--cols: {cols}">
    {#each cameraNames as name (name)}
      <CameraFeed {partID} {name} />
    {:else}
      <p class="empty">Cameras recorded by the capture controller appear here.</p>
    {/each}
    {#if session && active}
      <div class="badge" aria-hidden="true">{LABELS[session.phase]} {clock}</div>
    {/if}
  </section>

  <section class="drawer">
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
    {:else if session && active}
      <div class="status {session.phase}">
        <strong aria-live="polite">{LABELS[session.phase]}</strong>
        <span class="clock">{clock}</span>
        <span>Episode {session.episode} / {session.settings.episodes} · task:{session.settings.task}</span>
        <small>space: {session.phase === 'recording' ? 'end episode' : 'start next episode'} · esc: stop session</small>
      </div>
    {:else}
      {#if session}
        <p class="last">
          <strong class="done">DONE</strong> · {session.episode} / {session.settings.episodes} episodes · task:{session.settings.task}
        </p>
      {/if}
      <form
        onsubmit={(e) => {
          e.preventDefault();
          startSession();
        }}
      >
        <label class="wide">Task <input bind:value={settings.task} required placeholder="pick up the red cube" /></label>
        <label>Controller
          <select bind:value={controllerName} required>
            {#each controllers as c (c.name)}<option>{c.name}</option>{/each}
          </select>
        </label>
        <label>Episodes <input type="number" min="1" step="1" required bind:value={settings.episodes} /></label>
        <label>Episode length (s)
          <input type="number" min="1" placeholder="open-ended" bind:value={settings.episodeS} />
        </label>
        <label>Reset (s) <input type="number" min="0" required bind:value={settings.resetS} /></label>
        <label>FPS <input type="number" min="0.1" step="any" required bind:value={settings.fps} /></label>
        <button type="submit" disabled={!sensor || orphaned}>Start session</button>
      </form>
    {/if}
  </section>
</main>

<style>
  main { display: grid; grid-template-rows: minmax(0, 1fr) auto; height: 100dvh; }

  .cams {
    position: relative; min-height: 0; padding: 8px; gap: 8px; background: #000;
    display: grid; grid-template-columns: repeat(var(--cols), minmax(0, 1fr)); grid-auto-rows: minmax(0, 1fr);
  }
  .cams.recording :global(figure) { outline: 3px solid var(--rec); outline-offset: -3px; }
  .cams.reset :global(figure) { outline: 3px solid var(--reset); outline-offset: -3px; }
  .badge {
    position: absolute; top: 16px; left: 16px; padding: 4px 10px; border-radius: 4px;
    background: rgba(0, 0, 0, 0.6); color: #fff; font-weight: 700; font-variant-numeric: tabular-nums;
  }
  .cams.recording .badge { color: #ff6b6b; }
  .cams.reset .badge { color: #ffb84d; }
  .empty { color: #aaa; margin: auto; }

  .drawer { display: grid; gap: 8px; padding: 12px 16px; background: var(--panel); border-top: 1px solid var(--border); }
  form { display: flex; flex-wrap: wrap; align-items: end; gap: 8px 12px; }
  label { display: grid; gap: 4px; flex: 1 1 110px; min-width: 0; font-size: 13px; color: var(--muted); }
  label.wide { flex: 3 1 220px; }
  input, select, button { font: inherit; padding: 6px 8px; box-sizing: border-box; }
  input, select { width: 100%; min-width: 0; }
  form button { flex: 0 0 auto; }

  .status { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 16px; }
  .status strong { font-size: 20px; }
  .status.recording strong { color: var(--rec); }
  .status.reset strong { color: var(--reset); }
  .clock { font-size: 28px; font-variant-numeric: tabular-nums; }
  .last { margin: 0; }
  .done { color: var(--done); }
  .banner { margin: 0; padding: 8px 12px; border-radius: 6px; border: 1px solid; }
  .error { border-color: var(--rec); }
  .warn { border-color: var(--reset); }

  @media (max-width: 720px) {
    main { height: auto; }
    .cams { grid-template-columns: 1fr; grid-auto-rows: auto; }
  }
</style>
```

- [ ] **Step 4: (Already verified) An empty number input binds to `null`**

In Svelte 5, `to_number` in `node_modules/svelte/src/internal/client/dom/elements/bindings/input.js` returns `null` for `''`, and the engine treats `null` as open-ended. Nothing to do.

- [ ] **Step 5: Verify**

Run: `npm run check && npm run build && npm test`
Expected: 0 errors, the build succeeds, `# pass 5`.

- [ ] **Step 6: Commit**

```bash
git add src/CameraFeed.svelte src/Recorder.svelte
git commit -m "feat: camera-first layout, open-ended episode length, reconnect-safe camera feeds"
```

---

### Task 3: Manual check on the robot (user)

1. **Form:** at desktop width and at about 375px, no input overflows its column.
2. **Open-ended episode:** clear "Episode length (s)" and start. The clock counts up ("elapsed"), and Space ends the episode. Reset still counts down.
3. **Multiple cameras:** the grid uses 1, 2, or 2×2 columns depending on the camera count. The borders and badge go red while recording and amber during reset.
4. **Reconnect:** with cameras showing, interrupt the machine's network for about 10 s, or restart viam-server. Each feed shows "connecting…" and then recovers on its own within about 5 s of the connection coming back.
