<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { GenericServiceClient, type RobotClient } from '@viamrobotics/sdk';
  import * as rb from './robot.ts';
  import * as rn from './runner.ts';
  import CameraFeed from './CameraFeed.svelte';
  import Review from './Review.svelte';

  let {
    partID, robot, runner, cameraNames, tasks, activeTask, fps: defaultFps, canHome, home, onclose,
  }: {
    partID: string; robot: RobotClient; runner: string; cameraNames: string[]; tasks: string[]; activeTask: string;
    fps: number; canHome: boolean; home: () => Promise<void>; onclose: () => void;
  } = $props();

  const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));
  const client = $derived(new GenericServiceClient(robot, runner));
  // Commands that start or stop hardware can take a while (executor stop waits up to 15 s); reads stay short.
  const LONG_MS = 20_000;
  const call = (command: string, extra: Record<string, unknown> = {}, ms = rb.CALL_TIMEOUT_MS) =>
    rb.withTimeout(client.doCommand({ command, ...extra }), ms, `Runner ${command}`);

  let task = $state(untrack(() => activeTask));
  let checkpoint = $state(((): string => { try { return JSON.parse(localStorage.getItem('policyCheckpoint') ?? '""') ?? ''; } catch { return ''; } })());
  let fps = $state(untrack(() => defaultFps));
  let st = $state.raw<rn.RunnerStatus | undefined>();
  let error = $state(''); // last command failure
  let linkError = $state(''); // poll or heartbeat failure; cleared by the next good status
  let homing = $state(false);
  let showKeys = $state(false);
  let now = $state(Date.now());
  let ending = $state(false);
  let ended = $state.raw<{ id: string; segs: rn.Segment[]; error: string } | undefined>();
  let reviewing = $state(false);
  let lastId = ''; // from the last status that had a session: `stop` clears it

  const phase = $derived(st?.state ?? '');
  const idle = $derived(rn.isIdle(phase));
  const counts = $derived(st?.session?.counts ?? {});
  const tally = (o: string) => counts[`policy:${o}`] ?? 0;
  const sum = $derived(ended ? rn.summary(ended.segs) : undefined);
  const elapsed = $derived(st?.episode ? Math.max(0, Math.floor((now - st.episode.started) / 1000)) : 0);
  const cols = $derived(cameraNames.length <= 1 ? 1 : cameraNames.length <= 4 ? 2 : 3);
  const pct = (n: number) => `${Math.round(n * 100)}%`;
  const fmt = (t: number) => `${Math.floor(t / 60)}:${String(t % 60).padStart(2, '0')}`;

  let seq = 0, applied = 0; // out-of-order status replies must not roll the state back
  async function refresh() {
    const mine = ++seq;
    try {
      const s = rn.asRunnerStatus(await call('status'));
      if (!s) throw new Error(`${runner} did not answer like an episode runner`);
      if (mine < applied) return;
      applied = mine;
      st = s;
      linkError = '';
      if (s.session) lastId = s.session.id;
    } catch (e) {
      if (mine >= applied) linkError = msg(e);
    }
  }
  let inFlight = false; // fast double presses must not send duplicates; stop is exempt
  async function act(command: string, extra?: Record<string, unknown>) {
    if (command !== 'stop') {
      if (inFlight) return;
      inFlight = true;
    }
    try {
      await call(command, extra, LONG_MS);
      error = '';
    } catch (e) {
      error = `${command} failed: ${msg(e)}`;
    } finally {
      if (command !== 'stop') inFlight = false;
    }
    await refresh();
  }

  async function startSession(e: SubmitEvent) {
    e.preventDefault();
    try { localStorage.setItem('policyCheckpoint', JSON.stringify(checkpoint)); } catch {}
    ended = undefined;
    lastId = '';
    await act('start_session', { task, checkpoint, fps });
  }

  async function endSession() {
    if (ending) return;
    const id = st?.session?.id ?? lastId;
    ending = true;
    try {
      await call('stop', {}, LONG_MS); // the log is read only after stop has really finished
      error = '';
    } catch (e) {
      error = `stop failed: ${msg(e)}`;
    }
    if (!id) {
      ended = { id, segs: [], error: 'No session to summarize' };
    } else {
      try {
        const r = (await call('log', { session: id })) as { segments?: rn.Segment[] };
        ended = { id, segs: r.segments ?? [], error: '' };
      } catch (e) {
        ended = { id, segs: [], error: `Could not read the session log: ${msg(e)}` };
      }
    }
    lastId = '';
    ending = false;
    await refresh();
  }

  async function goHome() {
    if (!canHome || !idle || homing) return;
    homing = true;
    try { await home(); error = ''; } catch (e) { error = msg(e); }
    homing = false;
  }

  function onKey(e: KeyboardEvent) {
    if (e.repeat || e.ctrlKey || e.metaKey || e.altKey || reviewing || ending) return;
    if (e.target instanceof Element && e.target.closest('input, select, textarea')) return;
    const k = e.key.toLowerCase();
    if (e.key === '?') showKeys = !showKeys;
    else if (e.key === 'Escape') {
      showKeys = false;
      // An unknown status or any non-idle state may mean the robot is moving: always stop.
      if (!st || !idle || st.session) void endSession();
    } else if (showKeys) return;
    else if (e.code === 'Space') {
      e.preventDefault();
      if (phase === 'idle') void act('start_episode');
    } else if (k === 's' || k === 'f') {
      if (phase === 'policy' || phase === 'correction') void act('label', { outcome: k === 's' ? 'success' : 'failure' });
    } else if (k === 't') {
      if (phase === 'policy') void act('take_over');
    } else if (k === 'h') void goHome();
  }

  onMount(() => {
    // Status poll: next poll starts after the previous one settles.
    let poll: ReturnType<typeof setTimeout>;
    const loop = async () => {
      now = Date.now();
      await refresh();
      poll = setTimeout(loop, 500);
    };
    void loop();
    // Heartbeat ticks come from a worker: browsers throttle main-thread timers in background tabs.
    const beat = () => {
      if (st && rn.needsHeartbeat(st.state)) call('heartbeat').catch((e) => (linkError = `heartbeat failed: ${msg(e)}`));
    };
    let worker: Worker | undefined;
    let fallback: ReturnType<typeof setInterval> | undefined;
    try {
      const url = URL.createObjectURL(new Blob(['setInterval(() => postMessage(0), 500)'], { type: 'text/javascript' }));
      worker = new Worker(url);
      URL.revokeObjectURL(url);
      worker.onmessage = beat;
    } catch {
      fallback = setInterval(beat, 500); // ponytail: throttled in background tabs; only if workers are blocked
    }
    // Best effort: leaving with an episode running must not leave the arm going.
    const bye = () => { if (st?.episode) client.doCommand({ command: 'stop' }).catch(() => {}); };
    addEventListener('pagehide', bye);
    return () => {
      clearTimeout(poll);
      worker?.terminate();
      clearInterval(fallback);
      removeEventListener('pagehide', bye);
      bye();
    };
  });
</script>

<svelte:window onkeydown={onKey} />

<div class="ps" class:reviewing>
  {#if reviewing && ended}
    <Review {robot} session={{ id: ended.id, expected: rn.capturedCount(ended.segs) }} onclose={() => (reviewing = false)} />
  {:else}
    <section class="cams {phase}" style="--cols: {cols}">
      {#each cameraNames as name (name)}<CameraFeed {partID} {name} />{:else}<p class="empty">No cameras.</p>{/each}
      {#if st?.session}
        <div class="badge" aria-hidden="true">
          {phase === 'starting' ? 'Loading policy…' : phase.toUpperCase()}{st.episode ? ` · ${fmt(elapsed)}` : ''}
        </div>
      {/if}
      {#if homing}<div class="homing" aria-live="polite">Homing…</div>{/if}
    </section>

    <section class="drawer">
      {#if error}<p class="banner error" role="alert">{error}</p>{/if}
      {#if linkError}<p class="banner error" role="alert">{linkError}</p>{/if}
      {#if st?.episode}<p class="banner warn">Keep this tab open and visible: a closed or suspended tab stops the robot.</p>{/if}
      {#if st?.last_error}<p class="banner warn" role="alert">{st.last_error}</p>{/if}

      {#if !st}
        <p>Connecting to {runner}…</p>
      {:else if ended}
        <strong>Session {ended.id} ended</strong>
        {#if ended.error}<p class="banner error">{ended.error}</p>{/if}
        {#if sum}
          <p class="last">
            {#if sum.successRate === undefined}No judged episodes{:else}Success {pct(sum.successRate)} of {sum.success + sum.failure + sum.takeover} judged{/if}
            · {sum.episodes} policy episodes · {sum.corrections} corrections
          </p>
        {/if}
        <div class="row">
          <button type="button" disabled={rn.capturedCount(ended.segs) === 0} onclick={() => (reviewing = true)}>Review session</button>
          <button type="button" onclick={() => (ended = undefined)}>New session</button>
          <button type="button" onclick={onclose}>Back to recorder</button>
        </div>
      {:else if !st.session}
        <form onsubmit={startSession}>
          <label>Task
            <select bind:value={task} required>
              {#each tasks as t (t)}<option>{t}</option>{/each}
            </select>
          </label>
          <label>Checkpoint <input bind:value={checkpoint} required placeholder="e.g. act-v3" /></label>
          <label>FPS <input type="number" min="0.1" step="any" required bind:value={fps} /></label>
          <button type="submit" disabled={!task || !checkpoint.trim() || !idle}>Start session</button>
          <button type="button" onclick={onclose}>Back</button>
        </form>
        {#if !tasks.length}<small class="hint">Add a task in the recorder first.</small>{/if}
      {:else}
        <div class="status {phase}">
          <strong aria-live="polite">{phase === 'starting' ? 'Loading policy…' : phase}</strong>
          <span>Episode {st.episode?.id ?? st.session.episodes}{st.episode ? ` · ${st.episode.source}` : ''} · {fmt(elapsed)}</span>
          <span>task:{st.session.task} · {st.session.checkpoint}</span>
          <span>{tally('success')} success · {tally('failure')} failure · {tally('takeover')} takeover</span>
        </div>
        <div class="row">
          <button type="button" disabled={phase !== 'idle'} onclick={(e) => { void act('start_episode'); e.currentTarget.blur(); }}>Start episode <kbd>Space</kbd></button>
          <button type="button" disabled={phase !== 'policy' && phase !== 'correction'} onclick={(e) => { void act('label', { outcome: 'success' }); e.currentTarget.blur(); }}>Success <kbd>S</kbd></button>
          <button type="button" disabled={phase !== 'policy' && phase !== 'correction'} onclick={(e) => { void act('label', { outcome: 'failure' }); e.currentTarget.blur(); }}>Failure <kbd>F</kbd></button>
          <button type="button" disabled={phase !== 'policy'} onclick={(e) => { void act('take_over'); e.currentTarget.blur(); }}>Take over <kbd>T</kbd></button>
          <button type="button" disabled={!canHome || !idle || homing} onclick={(e) => { void goHome(); e.currentTarget.blur(); }}>Home <kbd>H</kbd></button>
          <button type="button" disabled={ending} onclick={(e) => { void endSession(); e.currentTarget.blur(); }}>End session <kbd>Esc</kbd></button>
          <small class="hint"><kbd>?</kbd> shortcuts</small>
        </div>
      {/if}
    </section>
  {/if}

  {#if showKeys}
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <div class="overlay" role="dialog" aria-label="Keyboard shortcuts" tabindex="-1" onclick={(e) => e.target === e.currentTarget && (showKeys = false)}>
      <table class="keys">
        <tbody>
          <tr><td>Space</td><td>Start the next policy episode</td></tr>
          <tr><td>S / F</td><td>Success / failure for the current run or correction</td></tr>
          <tr><td>T</td><td>Take over (policy running)</td></tr>
          <tr><td>H</td><td>Home arms (runner idle only)</td></tr>
          <tr><td>Esc</td><td>Close this overlay / stop the episode and session</td></tr>
          <tr><td>?</td><td>Show / hide shortcuts</td></tr>
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .ps { grid-row: 1 / -1; display: grid; grid-template-rows: minmax(0, 1fr) auto; min-height: 0; position: relative; }
  .ps.reviewing { grid-template-rows: minmax(0, 1fr); }
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .overlay { position: absolute; inset: 0; display: grid; place-items: center; background: rgba(0, 0, 0, 0.55); padding: 16px; }
  .keys { background: var(--panel); border-radius: 8px; padding: 12px 16px; border-collapse: collapse; }
  .keys td { padding: 4px 8px; border-top: 1px solid var(--border); }
  .keys td:first-child { font-weight: 600; white-space: nowrap; }
  kbd, .hint { font-size: 12px; color: var(--muted); }

  .cams {
    position: relative; min-height: 0; padding: 8px; gap: 8px; background: #000;
    display: grid; grid-template-columns: repeat(var(--cols), minmax(0, 1fr)); grid-auto-rows: minmax(0, 1fr);
  }
  .cams.policy :global(figure) { outline: 3px solid var(--rec); outline-offset: -3px; }
  .cams.correction :global(figure) { outline: 3px solid var(--reset); outline-offset: -3px; }
  .badge { position: absolute; top: 16px; left: 16px; padding: 4px 10px; border-radius: 4px; background: rgba(0, 0, 0, 0.6); color: #fff; font-weight: 700; font-variant-numeric: tabular-nums; }
  .empty { color: #aaa; margin: auto; }
  .homing { position: absolute; inset: 0; display: grid; place-items: center; background: rgba(0, 0, 0, 0.45); color: #fff; font-weight: 600; }

  .drawer { display: grid; gap: 8px; padding: 12px 16px; background: var(--panel); border-top: 1px solid var(--border); }
  form { display: flex; flex-wrap: wrap; align-items: end; gap: 8px 12px; }
  label { display: grid; gap: 4px; flex: 1 1 110px; min-width: 0; font-size: 13px; color: var(--muted); }
  input, select, button { font: inherit; padding: 6px 8px; box-sizing: border-box; }
  input, select { width: 100%; min-width: 0; }
  form button { flex: 0 0 auto; }
  .status { display: flex; flex-wrap: wrap; align-items: baseline; gap: 4px 16px; }
  .status strong { font-size: 20px; }
  .status.policy strong { color: var(--rec); }
  .status.correction strong { color: var(--reset); }
  .last { margin: 0; }
  .banner { margin: 0; padding: 8px 12px; border-radius: 6px; border: 1px solid; }
  .error { border-color: var(--rec); }
  .warn { border-color: var(--reset); }
  @media (max-width: 720px) { .cams { grid-template-columns: 1fr; grid-auto-rows: auto; } }
</style>
