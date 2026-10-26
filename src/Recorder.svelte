<script lang="ts">
  import { onMount } from 'svelte';
  import { MachineConnectionEvent, SensorClient } from '@viamrobotics/sdk';
  import { useConnectionStatus, useResourceNames, useRobotClient } from '@viamrobotics/svelte-sdk';
  import * as cap from './capture.ts';
  import CameraFeed from './CameraFeed.svelte';

  let { partID }: { partID: string } = $props();

  const robot = useRobotClient(() => partID);
  const status = useConnectionStatus(() => partID);
  const sensors = useResourceNames(() => partID, 'sensor');
  const cameras = useResourceNames(() => partID, 'camera');

  const DEFAULTS: cap.Settings = { task: '', episodes: 10, episodeS: null, resetS: 10, fps: 10 };
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
  // The svelte-sdk resets queries on disconnect (cameras.current → []); keep the last
  // known list so CameraFeed stays mounted and can recover its stream on reconnect.
  let cameraNames = $state<string[]>([]);
  $effect(() => {
    if (!cameras.query?.isSuccess) return;
    cameraNames = cameras.current.map((r) => r.name).filter((n) => controller?.resources.includes(n) ?? false);
  });
  const cols = $derived(cameraNames.length <= 1 ? 1 : cameraNames.length <= 4 ? 2 : 3);
  const active = $derived(session?.phase === 'recording' || session?.phase === 'reset');

  // Discover capture-control sensors by the shape of their readings.
  $effect(() => {
    const client = robot.current;
    const names = sensors.current.map((r) => r.name);
    if (!client || !connected || !sensors.query?.isSuccess) return;
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
  const fmt = (ms: number) => {
    const t = Math.floor(ms / 1000);
    return `${Math.floor(t / 60)}:${String(t % 60).padStart(2, '0')}`;
  };
  const clock = $derived.by(() => {
    if (!session) return '';
    const left = cap.remainingMs(session, now);
    return Number.isFinite(left) ? fmt(left + 999) : `${fmt(cap.elapsedMs(session, now))} elapsed`;
  });
</script>

<svelte:window onkeydown={onKey} />

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
