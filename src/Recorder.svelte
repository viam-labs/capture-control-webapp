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
      .then((r) => {
        if (!active) orphaned = cap.hasOpenSequence(r);
      })
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
      await send({ stop_capture: true }).catch(() => {});
      session = cap.stop(nextSession)[0];
    } finally {
      busy = false;
    }
  }

  function startSession() {
    if (busy) return;
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
    if (e.repeat) return;
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
      <div class="status {session.phase}">
        <strong aria-live="polite">{LABELS[session.phase]}</strong>
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
  .row { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; }
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
