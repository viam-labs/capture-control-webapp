<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import type { RobotClient, Sequence } from '@viamrobotics/sdk';
  import * as rv from './review.ts';
  import * as data from './data.ts';

  let {
    robot,
    session,
    onclose,
  }: { robot: RobotClient; session?: { id: string; expected: number }; onclose: () => void } = $props();

  const msg = (e: unknown) => (e instanceof Error ? e.message : String(e));

  // ---- Episode list ----
  let episodes = $state.raw<Sequence[]>([]);
  let localTags = $state.raw<Record<string, string[]>>({}); // tags we wrote, so a stale poll can't erase them
  let loading = $state(true);
  let error = $state('');
  let retry = $state(0);
  let sel = $state(0);
  let filter = $state({ task: '', session: '', review: '' });

  const tagsOf = (s: Sequence) => localTags[s.id] ?? s.sequenceTags;

  // Queue: one row per expected episode (undefined = not synced yet). History: newest first, filtered.
  const rows = $derived.by((): (Sequence | undefined)[] => {
    if (session) return Array.from({ length: Math.max(session.expected, episodes.length) }, (_, i) => episodes[i]);
    return [...episodes].reverse().filter((s) => {
      const t = rv.parseTags(tagsOf(s));
      return (
        (!filter.task || t.task === filter.task) &&
        (!filter.session || t.session === filter.session) &&
        (!filter.review || (filter.review === 'none' ? !t.review : t.review === filter.review))
      );
    });
  });
  const current = $derived(rows[sel]);
  const currentId = $derived(current?.id);
  const options = $derived.by(() => {
    const tags = episodes.map((s) => rv.parseTags(tagsOf(s)));
    return {
      tasks: [...new Set(tags.map((t) => t.task).filter(Boolean))] as string[],
      sessions: [...new Set(tags.map((t) => t.session).filter(Boolean))] as string[],
    };
  });

  // Poll as a setTimeout chain (never overlapping); the queue stops once every expected episode is listed.
  $effect(() => {
    retry; // dependency: Retry restarts the chain
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      try {
        const found = await data.listEpisodes({ ...(await data.ids(robot)), session: session?.id });
        if (stopped) return;
        episodes = found;
        if (error.startsWith("This machine's key")) error = ''; // don't wipe a tagging error
        if (session && found.length < session.expected) timer = setTimeout(poll, 5000);
      } catch (e) {
        if (!stopped) error = `This machine's key can't read captured data, or the request failed: ${msg(e)}`;
      } finally {
        if (!stopped) loading = false;
      }
    };
    poll();
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  });

  // ---- Player ----
  let tl = $state.raw<rv.Timeline>(new Map());
  let urls = $state.raw<Map<string, string>>(new Map()); // frame id → object URL
  let failed = $state.raw<Set<string>>(new Set()); // frame ids whose batch failed 3 times
  let frameCount = $state(0);
  let status = $state<'complete' | 'partial' | undefined>();
  let playhead = $state(0);
  let playing = $state(false);
  let loadGen = 0; // bumped only when the episode changes
  const inflight = new Set<string>();

  const sp = $derived(rv.span(tl));

  function revokeAll() {
    for (const u of urls.values()) URL.revokeObjectURL(u);
    urls = new Map();
  }
  onDestroy(revokeAll);

  // Fetch bytes from the playhead forward, 20 at a time, retrying a batch twice. Whole episode stays loaded.
  // ponytail: ~34 MB for 900 frames; add a window around the playhead if long episodes become a problem.
  async function loadBytes(gen: number, frames: rv.Frame[]) {
    const ahead = (fr: rv.Frame) => fr.time >= playhead;
    const todo = frames
      .filter((fr) => !urls.has(fr.id) && !failed.has(fr.id) && !inflight.has(fr.id))
      .sort((a, b) => (ahead(a) === ahead(b) ? a.time - b.time : ahead(a) ? -1 : 1));
    todo.forEach((fr) => inflight.add(fr.id)); // claim the whole list so a later refresh doesn't fetch it again
    for (let i = 0; i < todo.length; i += 20) {
      const batch = todo.slice(i, i + 20);
      let blobs: Map<string, Blob> | undefined;
      for (let attempt = 0; attempt < 3 && !blobs; attempt++) blobs = await data.frameBytes(batch).catch(() => undefined);
      if (gen !== loadGen) return; // stale: never create URLs, and don't touch the new episode's inflight
      batch.forEach((fr) => inflight.delete(fr.id));
      if (!blobs) {
        failed = new Set([...failed, ...batch.map((fr) => fr.id)]);
        continue;
      }
      const next = new Map(urls);
      for (const [id, b] of blobs) next.set(id, URL.createObjectURL(b));
      urls = next;
    }
  }

  // Load the selected episode; re-fetch the frame index every 5 s until it's complete (keeps going while partial).
  $effect(() => {
    const id = currentId;
    const gen = ++loadGen;
    untrack(() => {
      revokeAll();
      inflight.clear();
      tl = new Map();
      failed = new Set();
      frameCount = 0;
      status = undefined;
      playing = false;
    });
    if (!id) return;
    const end = data.ms(untrack(() => current)?.endTime);
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stale = 0;
    const refresh = async () => {
      try {
        const frames = await data.frameIndex(id);
        if (gen !== loadGen) return;
        stale = frames.length === frameCount ? stale + 1 : 0;
        frameCount = frames.length;
        const next = rv.timeline(frames);
        const s = rv.span(next);
        if (s && !tl.size) playhead = s.start;
        tl = next;
        status = rv.playable(frames.length, s?.end ?? 0, end, stale);
        if (error.startsWith("Couldn't load frames")) error = '';
        loadBytes(gen, frames);
      } catch (e) {
        if (gen === loadGen) error = `Couldn't load frames: ${msg(e)}`;
      }
      if (gen === loadGen && status !== 'complete') timer = setTimeout(refresh, 5000);
    };
    refresh();
    return () => {
      loadGen++;
      clearTimeout(timer);
    };
  });

  // Real-time playback.
  $effect(() => {
    if (!playing) return;
    let last = performance.now();
    let raf = requestAnimationFrame(function tick(now) {
      const end = sp?.end ?? playhead;
      playhead = Math.min(playhead + (now - last), end);
      last = now;
      if (playhead >= end) playing = false;
      else raf = requestAnimationFrame(tick);
    });
    return () => cancelAnimationFrame(raf);
  });

  // Cameras' timestamps are offset: before a camera's first frame, show that first frame rather than "loading…".
  const shown = $derived(
    [...tl].map(([camera, frames]) => {
      const fr = rv.loadedAt(frames, Math.max(playhead, frames[0].time), (id) => urls.has(id));
      return { camera, url: fr && urls.get(fr.id) };
    }),
  );

  // 50-bin loaded band: loaded / missing / failed, drawn as a CSS gradient behind the scrubber.
  const band = $derived.by(() => {
    if (!sp || sp.end === sp.start) return 'none';
    const all = [...tl.values()].flat();
    const bins = Array.from({ length: 50 }, (_, b) => {
      const lo = sp.start + ((sp.end - sp.start) * b) / 50;
      const hi = sp.start + ((sp.end - sp.start) * (b + 1)) / 50;
      const inBin = all.filter((fr) => fr.time >= lo && (fr.time < hi || (b === 49 && fr.time <= hi)));
      if (inBin.some((fr) => failed.has(fr.id))) return 'var(--rec)';
      return inBin.every((fr) => urls.has(fr.id)) ? 'var(--loaded)' : 'transparent';
    });
    return `linear-gradient(to right, ${bins.map((c, b) => `${c} ${b * 2}% ${(b + 1) * 2}%`).join(', ')})`;
  });

  // ---- Actions ----
  let tagging = false;
  async function tag(state: rv.Review) {
    const seq = current;
    if (!seq || !status || tagging) return;
    tagging = true;
    try {
      const tags = await data.setReview(seq.id, state);
      // Pick the next episode by id from the rows as they were before tagging: a review filter may drop the
      // tagged row and shift indices, and the user may have moved the selection while the request was pending.
      const before = rows;
      localTags = { ...localTags, [seq.id]: tags };
      const n = rv.nextUnreviewed(
        before.map((s) => ({ listed: !!s, review: s && rv.parseTags(tagsOf(s)).review })),
        before.findIndex((s) => s?.id === seq.id),
      );
      sel = n >= 0 ? rows.findIndex((s) => s?.id === before[n]?.id) : Math.max(0, Math.min(sel, rows.length - 1));
    } catch (e) {
      error = `Tagging failed: ${msg(e)}`;
    } finally {
      tagging = false;
    }
  }

  function move(d: 1 | -1) {
    sel = Math.max(0, Math.min(rows.length - 1, sel + d));
  }

  function toggle() {
    if (!sp) return;
    if (!playing && playhead >= sp.end) playhead = sp.start;
    playing = !playing;
  }

  function onKey(e: KeyboardEvent) {
    if (e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target instanceof Element && e.target.closest('input:not([type=range]), select, textarea')) return;
    const k = e.key;
    if (k === 'k' || k === 'K') tag('keep');
    else if (k === 'd' || k === 'D') tag('discard');
    else if (k === 'ArrowRight' || k === 'ArrowLeft') {
      e.preventDefault();
      move(k === 'ArrowRight' ? 1 : -1);
    } else if (e.code === 'Space') {
      e.preventDefault();
      toggle();
    } else if (k === ',' || k === '.') {
      playing = false;
      playhead = rv.step(tl, playhead, k === '.' ? 1 : -1);
    } else if (k === 'Escape') onclose();
  }

  const LABEL = { keep: '✓ keep', discard: '✗ discard' };
  const fmt = (t: number) => {
    const d = Math.round(t / 100); // tenths of a second, so 59.96 s shows as 1:00.0, not 0:60.0
    return `${Math.floor(d / 600)}:${((d % 600) / 10).toFixed(1).padStart(4, '0')}`;
  };
  const blurAfter = (fn: () => void) => (e: MouseEvent & { currentTarget: HTMLElement }) => {
    fn();
    e.currentTarget.blur(); // else a later Space/Enter re-clicks it
  };
</script>

<svelte:window onkeydown={onKey} />

<section class="review">
  <div class="stage" style="--cols: {Math.min(Math.max(shown.length, 1), 3)}">
    {#each shown as c (c.camera)}
      <figure>
        {#if c.url}<img src={c.url} alt="{c.camera} frame" />{:else}<span>loading…</span>{/if}
        <figcaption>{c.camera}</figcaption>
      </figure>
    {:else}
      <p class="empty">
        {#if current}Syncing frames ({frameCount})…
        {:else if loading}Loading episodes…
        {:else if session}Waiting for this session's episodes to sync…
        {:else if episodes.length}No episodes match these filters.
        {:else}No episodes recorded on this machine yet.{/if}
      </p>
    {/each}
  </div>

  {#if sp}
    <div class="transport">
      <button type="button" aria-label={playing ? 'Pause' : 'Play'} onclick={blurAfter(toggle)}>{playing ? '❚❚' : '▶'}</button>
      <div class="scrub" style="--band: {band}">
        <input
          type="range"
          aria-label="Playhead"
          min={sp.start}
          max={sp.end}
          step="any"
          bind:value={playhead}
          oninput={() => (playing = false)}
          onchange={(e) => e.currentTarget.blur()}
        />
      </div>
      <span class="time">{fmt(playhead - sp.start)} / {fmt(sp.end - sp.start)}</span>
    </div>
  {/if}

  <div class="panel">
    {#if error}
      <p class="banner error" role="alert">
        {error}
        <button type="button" onclick={blurAfter(() => ((error = ''), retry++))}>Retry</button>
      </p>
    {/if}

    <div class="row">
      <strong>{session ? `Review · session ${session.id}` : 'Episodes'}</strong>
      {#if session}
        <small class="hint">
          {rows.filter((s) => s && rv.parseTags(tagsOf(s)).review).length} of {session.expected} reviewed
          {#if episodes.length < session.expected}· waiting for {session.expected - episodes.length} to sync{/if}
        </small>
      {:else}
        <select aria-label="Task" bind:value={filter.task} onchange={(e) => { sel = 0; e.currentTarget.blur(); }}>
          <option value="">All tasks</option>
          {#each options.tasks as t (t)}<option>{t}</option>{/each}
        </select>
        <select aria-label="Session" bind:value={filter.session} onchange={(e) => { sel = 0; e.currentTarget.blur(); }}>
          <option value="">All sessions</option>
          {#each options.sessions as s (s)}<option>{s}</option>{/each}
        </select>
        <select aria-label="Review status" bind:value={filter.review} onchange={(e) => { sel = 0; e.currentTarget.blur(); }}>
          <option value="">Any status</option>
          <option value="none">Not reviewed</option>
          <option value="keep">Keep</option>
          <option value="discard">Discard</option>
        </select>
      {/if}
      <button type="button" class="close" onclick={blurAfter(onclose)}>Close <kbd>esc</kbd></button>
    </div>

    <div class="eps">
      {#each rows as s, i (s?.id ?? `pending-${i}`)}
        {@const t = s && rv.parseTags(tagsOf(s))}
        <button type="button" class="ep" class:sel={i === sel} onclick={blurAfter(() => (sel = i))}>
          <span class="n">#{session ? i + 1 : episodes.indexOf(s!) + 1}</span>
          <span>{t?.task ?? '…'}{#if !session && t?.session}<small> · session {t.session}</small>{/if}</span>
          <small>{s ? `${((data.ms(s.endTime) - data.ms(s.startTime)) / 1000).toFixed(1)} s` : ''}</small>
          <span class="st {t?.review ?? (s ? 'none' : 'sync')}">{t?.review ? LABEL[t.review] : s ? 'not reviewed' : '⟳ syncing…'}</span>
        </button>
      {/each}
    </div>

    <div class="row">
      <button type="button" class="keep" disabled={!status} onclick={blurAfter(() => tag('keep'))}>Keep <kbd>K</kbd></button>
      <button type="button" class="discard" disabled={!status} onclick={blurAfter(() => tag('discard'))}>Discard <kbd>D</kbd></button>
      <small class="hint">
        {#if current && !status}syncing frames ({frameCount})…{:else if status === 'partial'}partial: some frames may be missing{/if}
        {#if failed.size}· {failed.size} frames failed to load{/if}
        · <kbd>←</kbd><kbd>→</kbd> episode · <kbd>space</kbd> play · <kbd>,</kbd><kbd>.</kbd> step
      </small>
    </div>
  </div>
</section>

<style>
  .review { display: grid; grid-template-rows: minmax(0, 1fr) auto auto; min-height: 0; height: 100%; --loaded: color-mix(in srgb, var(--done) 45%, transparent); }
  .stage {
    min-height: 0; padding: 8px; gap: 8px; background: #000;
    display: grid; grid-template-columns: repeat(var(--cols), minmax(0, 1fr)); grid-auto-rows: minmax(0, 1fr);
  }
  figure { position: relative; margin: 0; min-height: 0; display: grid; place-items: center; background: #111; color: #aaa; }
  figure img { place-self: stretch; width: 100%; height: 100%; min-height: 0; object-fit: contain; }
  figcaption { position: absolute; left: 8px; bottom: 6px; font-size: 12px; color: #ddd; text-shadow: 0 0 3px #000; }
  .empty { color: #aaa; margin: auto; }
  .transport { display: flex; align-items: center; gap: 10px; padding: 6px 16px; background: var(--panel); border-top: 1px solid var(--border); }
  .scrub { flex: 1; min-width: 0; background: var(--band); border-radius: 999px; }
  .scrub input { width: 100%; margin: 0; }
  .time { font-size: 12px; color: var(--muted); font-variant-numeric: tabular-nums; white-space: nowrap; }
  .panel { display: grid; gap: 8px; padding: 12px 16px; background: var(--panel); border-top: 1px solid var(--border); }
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 12px; }
  .close { margin-left: auto; }
  .eps { display: grid; gap: 4px; max-height: 30vh; overflow-y: auto; }
  .ep {
    display: grid; grid-template-columns: 4ch minmax(0, 1fr) auto auto; gap: 12px; align-items: center;
    padding: 6px 10px; border: 1px solid var(--border); border-radius: 6px; background: none; text-align: left; cursor: pointer; font: inherit; color: inherit;
  }
  .ep.sel { border-color: currentColor; font-weight: 600; }
  .ep small, .n { color: var(--muted); font-variant-numeric: tabular-nums; }
  .st { font-size: 12px; white-space: nowrap; }
  .st.keep { color: var(--done); } .st.discard { color: var(--rec); } .st.sync { color: var(--reset); } .st.none { color: var(--muted); }
  button.keep, button.discard { font-weight: 600; }
  button.keep:not(:disabled) { color: var(--done); } button.discard:not(:disabled) { color: var(--rec); }
  .hint { color: var(--muted); font-size: 12px; }
  .hint kbd { border: 1px solid var(--border); border-radius: 3px; padding: 0 4px; font-size: 11px; }
  .banner { margin: 0; padding: 8px 12px; border-radius: 6px; border: 1px solid var(--rec); }
</style>
