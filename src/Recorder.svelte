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

  let controllers = $state<{ name: string; resources: string[] }[]>([]);
  let controllerName = $state('');
  let settings = $state<cap.Settings>({ ...DEFAULTS, ...storedSettings });
  let session = $state.raw<cap.Session | undefined>();
  let now = $state(Date.now());
  let error = $state('');
  let orphaned = $state(false);

  // Saved tasks (localStorage). activeTask is what the next episode records.
  let tasks = $state<string[]>(load('tasks', v1Task ? [v1Task] : []));
  let activeTask = $state<string>(load('activeTask', ''));
  let formEl = $state<HTMLFormElement>();
  $effect(() => save('tasks', $state.snapshot(tasks)));
  $effect(() => save('activeTask', activeTask));
  $effect(() => {
    if (!tasks.includes(activeTask)) activeTask = tasks[0] ?? '';
  });

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
  // While recording, the open episode's chip is highlighted; a different pick shows as queued ("next").
  const shownTask = $derived(session?.phase === 'recording' ? session.episodeTask : activeTask);
  const queued = $derived(session?.phase === 'recording' && activeTask !== session.episodeTask);
  const canStart = $derived(!!sensor && !orphaned && !!activeTask);

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
      // nextSession already counts every episode that ended; a failed start_capture never opened one.
      session = { ...nextSession, phase: 'done' };
    } finally {
      busy = false;
    }
  }

  function startSession() {
    if (busy || !canStart) return;
    error = '';
    advanceRequested = stopRequested = false; // drop keys pressed during a previous session
    const snapshot = $state.snapshot(settings);
    save('settings', snapshot);
    apply(cap.start(snapshot, activeTask, crypto.randomUUID().slice(0, 8), Date.now()));
  }

  function stopOrphan() {
    send({ stop_capture: true })
      .then(() => (orphaned = false))
      .catch((e) => (error = `Stop failed: ${e instanceof Error ? e.message : e}`));
  }

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
        await apply(cap.next(session, now, activeTask));
      } else {
        await apply(cap.tick(session, now, activeTask));
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
        <span>Episode {session.episode} / {session.settings.episodes} · task:{session.episodeTask}</span>
        <small>space: {session.phase === 'recording' ? 'end episode' : 'start next episode'} · esc: stop session</small>
      </div>
      {@render palette()}
    {:else}
      {#if session}
        <p class="last">
          <strong class="done">DONE</strong> · {session.episode} / {session.settings.episodes} episodes
        </p>
      {/if}
      {@render palette()}
      <form
        bind:this={formEl}
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
        <label>Episodes <input type="number" min="1" step="1" required bind:value={settings.episodes} /></label>
        <label>Episode length (s)
          <input type="number" min="1" placeholder="open-ended" bind:value={settings.episodeS} />
        </label>
        <label>Reset (s) <input type="number" min="0" required bind:value={settings.resetS} /></label>
        <label>FPS <input type="number" min="0.1" step="any" required bind:value={settings.fps} /></label>
        <button type="submit" disabled={!canStart}>Start session</button>
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
  input, select, button { font: inherit; padding: 6px 8px; box-sizing: border-box; }
  input, select { width: 100%; min-width: 0; }
  form button { flex: 0 0 auto; }

  .palette { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
  .chip { display: inline-flex; align-items: center; border: 1px solid var(--border); border-radius: 999px; overflow: hidden; }
  .chip button { border: 0; background: none; padding: 4px 10px; display: inline-flex; gap: 6px; align-items: baseline; cursor: pointer; }
  .chip button.x { padding: 4px 8px 4px 0; color: var(--muted); }
  .chip.active { border-color: currentColor; font-weight: 600; }
  .chip.queued { border-style: dashed; }
  .chip kbd, .chip small { font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
  .chip em { font-size: 10px; font-style: normal; text-transform: uppercase; letter-spacing: 0.05em; }
  .add { width: 150px; border: 1px dashed var(--border); border-radius: 999px; }

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
