# Episode Review (v2 spec B) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two features. First, an end-of-session review queue, which plays each episode's camera frames and tags it `review:keep` or `review:discard`. Second, an Episodes history view that uses the same player and the same tagging.

**Spec:** `docs/superpowers/specs/2026-10-26-episode-review-design.md`. Read its **Decisions**, **Units** and **Failure handling** sections first.

**Architecture:** The browser calls the Viam Data API directly, using the machine's own cookie credentials. There are no module changes.

| File | Status | Responsibility |
|---|---|---|
| `src/review.ts` | new | Pure helpers for tags, the frame timeline, stepping, playability and navigation. |
| `src/data.ts` | new | Thin SDK layer. |
| `src/Review.svelte` | new | Episode list, polling, tagging, keys, and the player. |
| `src/Recorder.svelte` | modified | Entry buttons. Swaps `<Review>` in place of the cameras and drawer. Keeps the banners visible. |

**Tech Stack:** Svelte 5 runes, `@viamrobotics/sdk` 0.76 (`createViamClient`, `DataClient`, `RobotClient.getCloudMetadata`), and `node:test` running under Node 24 type stripping.

**Facts the engineer needs**

- **Data API calls.** All of these are verified on pi-arm6 with a machine-scoped key.
  - `dataClient.listSequences(orgId, pageToken?, pageSize?)` returns `{ sequences: Sequence[], nextPageToken }`. It is org-wide and has no filter.
  - `getSequence(id)` returns a `Sequence`.
  - `getSequenceBinaryData(id, pageToken?, pageSize?)` returns `{ data: BinaryData[], nextPageToken }`. This is **metadata only**: `binary` is empty.
  - `binaryDataByIds(ids, true)` returns `BinaryData[]` with the bytes. The order is **not** guaranteed.
  - `updateSequence(id, resources?, sequenceTags?, startTime?, endTime?, fieldMask?)`.
- **`Sequence` fields:** `id`, `partId`, `sequenceTags: string[]`, and `startTime?` / `endTime?`, which are protobuf `Timestamp` objects. Convert them with `.toDate().getTime()`.
- **`BinaryData` fields:** `binary: Uint8Array` and `metadata`. The metadata includes `binaryDataId`, `captureMetadata.componentName`, `captureMetadata.mimeType`, and `timeRequested?` / `timeReceived?` (both `Timestamp`).
- **Robot client:** `robot.getCloudMetadata()` returns `{ primaryOrgId, machinePartId, … }`.
- **`createViamClient({ credentials })`:** it accepts `auth.js`'s `{ type: 'api-key', payload, authEntity }`. `serviceHost` defaults to app.viam.com.
- **Existing patterns in `Recorder.svelte`:**
  - Key guards: `e.repeat`, the modifier keys, and focus inside an input.
  - `e.currentTarget.blur()` after every button click.
  - `$state.raw` for immutable objects.
  - Generation counters (`pollSeq`) to drop stale async results.
  - `ponytail:` comments mark known ceilings.
- **Commands:** `npm test` runs `node --test src/*.test.ts`. Use `npm run check` for svelte-check and `npm run build` for vite.

**Simplification relative to the spec:** the scrubber's band shows loaded frames in 50 bins, drawn as a CSS gradient: loaded, missing, or failed. A failed batch shows as red bins, and the count appears in the status text.

---

## File map

| File | Change |
|---|---|
| `src/review.ts` | New: `parseTags`, `withReview`, `timeline`, `frameAt`, `loadedAt`, `step`, `span`, `playable`, `nextUnreviewed`. |
| `src/review.test.ts` | New. |
| `src/data.ts` | New: `client`, `ids`, `ms`, `listEpisodes`, `frameIndex`, `frameBytes`, `setReview`. |
| `src/Review.svelte` | New. |
| `src/Recorder.svelte` | Add the Review entry buttons, the swap, the `banners` snippet, and the `onKey` early return. |
| `README.md` | Add a short "Reviewing episodes" section. |

---

### Task 1: Gate: check reads and writes with the real cookie key (operator, by hand)

The spec requires this check to pass before any UI work. **If it fails, stop.** Report the failure, then revisit the spec's Data access decision.

- [ ] **Step 1:** Open the app the way operators use it: through viamapplications.com, or through the module's local server.
- [ ] **Step 2:** In the browser devtools console, read the key.
  - **Local server:** run `document.cookie` and copy the `api-key-id` and `api-key` values.
  - **viamapplications.com:** run `JSON.parse(decodeURIComponent(document.cookie.split('; ').find(c => c.startsWith(location.pathname.split('/')[2] + '=')).split('=').slice(1).join('=')))`. The result has an `apiKey: { id, key }` field.
- [ ] **Step 3:** In **your own terminal**, not through Claude, run:
  ```bash
  VIAM_KEY_ID=<id> VIAM_KEY=<key> node /private/tmp/claude-501/-Users-nick-hehr-src-robot-demo-capture-controller/ac8fba35-377f-446e-851f-7101b08cfddf/scratchpad/probe/probe.mjs --write
  ```
  Running it yourself keeps the key out of the transcript. The `--write` step adds `review:probe` to the `pick up the pen` sequence, confirms the tag is there, and then restores the original tags.
- [ ] **Step 4:** Paste back the ✅ and ❌ lines.
  - **Pass:** every read and `updateSequence` show ✅, and the line ends with the original tags restored.
  - **Fail:** any line shows ❌. Stop here.

---

### Task 2: `review.ts` pure helpers (TDD)

**Files:**
- Create: `src/review.ts`, `src/review.test.ts`

- [ ] **Step 1: Write the failing tests**

Create `src/review.test.ts`:

```ts
import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as rv from './review.ts';

const f = (camera: string, time: number): rv.Frame => ({ id: `${camera}@${time}`, camera, mime: 'image/jpeg', time });
// Two cameras with offset timestamps.
const frames = [f('front', 100), f('wrist', 150), f('front', 200), f('wrist', 250), f('front', 300)];

test('parseTags reads task, session and review', () => {
  assert.deepEqual(rv.parseTags(['task:pick up the pen', 'session:abc', 'fps:10', 'review:keep']), {
    task: 'pick up the pen',
    session: 'abc',
    review: 'keep',
  });
  assert.deepEqual(rv.parseTags(['task:x', 'review:maybe']), { task: 'x', session: undefined, review: undefined });
});

test('withReview replaces any earlier review tag and keeps the others in order', () => {
  assert.deepEqual(rv.withReview(['task:x', 'review:keep', 'session:s'], 'discard'), ['task:x', 'session:s', 'review:discard']);
  assert.deepEqual(rv.withReview(['task:x'], 'keep'), ['task:x', 'review:keep']);
});

test('timeline groups by camera, sorted by time; frameAt picks the last frame at or before t', () => {
  const tl = rv.timeline([...frames].reverse());
  assert.deepEqual([...tl.keys()].sort(), ['front', 'wrist']);
  const front = tl.get('front')!;
  assert.deepEqual(front.map((x) => x.time), [100, 200, 300]);
  assert.equal(rv.frameAt(front, 99), undefined); // before the first frame
  assert.equal(rv.frameAt(front, 100)?.time, 100); // exactly on a frame
  assert.equal(rv.frameAt(front, 250)?.time, 200); // between frames
  assert.equal(rv.frameAt(front, 999)?.time, 300); // past the end
});

test('loadedAt holds the last loaded frame while later ones download', () => {
  const front = rv.timeline(frames).get('front')!;
  const has = (id: string) => id !== 'front@300';
  assert.equal(rv.loadedAt(front, 350, has)?.time, 200);
  assert.equal(rv.loadedAt(front, 50, has), undefined);
});

test('step moves to the next/previous distinct frame time across cameras; span is min..max', () => {
  const tl = rv.timeline(frames);
  assert.equal(rv.step(tl, 100, 1), 150);
  assert.equal(rv.step(tl, 175, 1), 200);
  assert.equal(rv.step(tl, 200, -1), 150);
  assert.equal(rv.step(tl, 300, 1), 300); // at the end: stays
  assert.equal(rv.step(tl, 100, -1), 100); // at the start: stays
  assert.deepEqual(rv.span(tl), { start: 100, end: 300 });
  assert.equal(rv.span(new Map()), undefined);
});

test('playable: complete near the sequence end, partial after 3 stale polls, never with 0 frames', () => {
  assert.equal(rv.playable(0, 0, 10_000, 9), undefined);
  assert.equal(rv.playable(40, 9_200, 10_000, 0), 'complete');
  assert.equal(rv.playable(20, 5_000, 10_000, 2), undefined);
  assert.equal(rv.playable(20, 5_000, 10_000, 3), 'partial');
});

test('nextUnreviewed wraps, skips placeholders and reviewed episodes, -1 when none are left', () => {
  const eps = [
    { listed: true, review: 'keep' as const },
    { listed: true },
    { listed: false },
    { listed: true },
  ];
  assert.equal(rv.nextUnreviewed(eps, 0), 1);
  assert.equal(rv.nextUnreviewed(eps, 1), 3);
  assert.equal(rv.nextUnreviewed(eps, 3), 1); // wraps
  assert.equal(rv.nextUnreviewed([{ listed: true, review: 'discard' as const }, { listed: false }], 0), -1);
});
```

- [ ] **Step 2: Run the tests and confirm they fail**

Run: `npm test`
Expected: FAIL with `Cannot find module './review.ts'`.

- [ ] **Step 3: Implement**

Create `src/review.ts`:

```ts
// Episode-review helpers. Pure: no Svelte or SDK imports. Frame times are epoch ms.

export type Review = 'keep' | 'discard';
export type Frame = { id: string; camera: string; mime: string; time: number };
export type Timeline = Map<string, Frame[]>;

export function parseTags(tags: string[]): { task?: string; session?: string; review?: Review } {
  const get = (key: string) => tags.find((t) => t.startsWith(`${key}:`))?.slice(key.length + 1);
  const review = get('review');
  return { task: get('task'), session: get('session'), review: review === 'keep' || review === 'discard' ? review : undefined };
}

export const withReview = (tags: string[], state: Review): string[] => [
  ...tags.filter((t) => !t.startsWith('review:')),
  `review:${state}`,
];

export function timeline(frames: Frame[]): Timeline {
  const tl: Timeline = new Map();
  for (const fr of frames) {
    const list = tl.get(fr.camera) ?? [];
    list.push(fr);
    tl.set(fr.camera, list);
  }
  for (const list of tl.values()) list.sort((a, b) => a.time - b.time);
  return tl;
}

// Index of the last frame with time <= t, or -1 (binary search; frames sorted by time).
function indexAt(frames: Frame[], t: number): number {
  let lo = 0;
  let hi = frames.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (frames[mid].time <= t) lo = mid + 1;
    else hi = mid;
  }
  return lo - 1;
}

export const frameAt = (frames: Frame[], t: number): Frame | undefined => frames[indexAt(frames, t)];

/** The last frame at or before t whose bytes are loaded: holds the picture while later frames download. */
export function loadedAt(frames: Frame[], t: number, has: (id: string) => boolean): Frame | undefined {
  for (let i = indexAt(frames, t); i >= 0; i--) if (has(frames[i].id)) return frames[i];
  return undefined;
}

// ponytail: recomputed per call (fine for a few thousand frames); memoize per timeline if episodes get long.
const times = (tl: Timeline): number[] => [...new Set([...tl.values()].flat().map((fr) => fr.time))].sort((a, b) => a - b);

/** Next (dir 1) or previous (dir -1) distinct frame time across all cameras; t itself at either end. */
export function step(tl: Timeline, t: number, dir: 1 | -1): number {
  const ts = times(tl);
  return (dir === 1 ? ts.find((x) => x > t) : ts.filter((x) => x < t).at(-1)) ?? t; // no findLast: tsconfig targets ES2022
}

export function span(tl: Timeline): { start: number; end: number } | undefined {
  const ts = times(tl);
  return ts.length ? { start: ts[0], end: ts[ts.length - 1] } : undefined;
}

/**
 * JPEGs sync separately from the sequence record. Complete once the last frame reaches the
 * sequence end; partial once the frame count has stopped growing for 3 polls; never with no frames.
 */
export function playable(count: number, lastTime: number, endTime: number, stalePolls: number): 'complete' | 'partial' | undefined {
  if (count === 0) return undefined;
  if (lastTime >= endTime - 1000) return 'complete';
  return stalePolls >= 3 ? 'partial' : undefined;
}

/** Next listed, unreviewed episode after `from` (wrapping); -1 if none. */
export function nextUnreviewed(eps: { listed: boolean; review?: Review }[], from: number): number {
  for (let k = 1; k < eps.length; k++) {
    const i = (from + k) % eps.length;
    if (eps[i].listed && !eps[i].review) return i;
  }
  return -1;
}
```

- [ ] **Step 4: Run the tests and confirm they pass**

Run: `npm test && npm run check`
Expected: all tests pass (14 existing + 7 new = 21), and check reports 0 errors.

- [ ] **Step 5: Commit**

```bash
git add src/review.ts src/review.test.ts
git commit -m "feat: pure episode-review helpers (tags, timeline, stepping, playability)"
```

---

### Task 3: `data.ts` Data API layer

**Files:**
- Create: `src/data.ts`

These are SDK calls with no logic worth unit testing. svelte-check verifies the field names and signatures.

- [ ] **Step 1: Implement**

Create `src/data.ts`:

```ts
// Thin Data API layer for episode review. The browser uses the machine's own cookie credentials,
// which can read and tag this machine's sequences (verified on pi-arm6; see the spec).
import { createViamClient, type Credentials, type RobotClient, type Sequence, type ViamClient } from '@viamrobotics/sdk';
import { getHostAndCredentials } from './auth.js';
import { withReview, type Frame, type Review } from './review.ts';

export type Ids = { orgId: string; partId: string };

// Cache the promise so concurrent first calls share one transport; drop it on failure so Retry starts fresh.
let clientP: Promise<ViamClient> | undefined;
export function client(): Promise<ViamClient> {
  // auth.js is plain JS (type: string), so the cast is required for svelte-check.
  return (clientP ??= createViamClient({ credentials: getHostAndCredentials().credentials as Credentials }).catch((e) => {
    clientP = undefined;
    throw e;
  }));
}

let idsP: Promise<Ids> | undefined;
export function ids(robot: RobotClient): Promise<Ids> {
  return (idsP ??= robot
    .getCloudMetadata()
    .then((m) => ({ orgId: m.primaryOrgId, partId: m.machinePartId }))
    .catch((e) => {
      idsP = undefined;
      throw e;
    }));
}

export const ms = (t?: { toDate(): Date }): number => t?.toDate().getTime() ?? 0;

/** This machine's sequences (optionally one session's), oldest first. */
export async function listEpisodes({ orgId, partId, session }: Ids & { session?: string }): Promise<Sequence[]> {
  const data = (await client()).dataClient;
  const out: Sequence[] = [];
  let page: string | undefined;
  // ponytail: listSequences has no part filter, so this scans the whole org on every call.
  // Add a start-time cutoff or cache seen pages if the org grows.
  do {
    const r = await data.listSequences(orgId, page, 100);
    out.push(...r.sequences.filter((s) => s.partId === partId && (!session || s.sequenceTags.includes(`session:${session}`))));
    page = r.nextPageToken || undefined;
  } while (page);
  return out.sort((a, b) => ms(a.startTime) - ms(b.startTime));
}

/** Image frame metadata for a sequence (no bytes). */
export async function frameIndex(seqId: string): Promise<Frame[]> {
  const data = (await client()).dataClient;
  const out: Frame[] = [];
  let page: string | undefined;
  do {
    const r = await data.getSequenceBinaryData(seqId, page, 100);
    for (const d of r.data) {
      const m = d.metadata;
      const cm = m?.captureMetadata;
      if (!m || !cm?.mimeType.startsWith('image/')) continue;
      out.push({ id: m.binaryDataId, camera: cm.componentName, mime: cm.mimeType, time: ms(m.timeRequested ?? m.timeReceived) });
    }
    page = r.nextPageToken || undefined;
  } while (page);
  return out;
}

/** Bytes for up to ~20 frames, keyed by binary data id (the result order isn't guaranteed). */
export async function frameBytes(frames: Frame[]): Promise<Map<string, Blob>> {
  const data = (await client()).dataClient;
  const mime = new Map(frames.map((fr) => [fr.id, fr.mime]));
  const out = new Map<string, Blob>();
  for (const d of await data.binaryDataByIds(frames.map((fr) => fr.id), true)) {
    const id = d.metadata?.binaryDataId;
    if (id) out.set(id, new Blob([new Uint8Array(d.binary)], { type: mime.get(id) ?? 'image/jpeg' }));
  }
  return out;
}

/** Swaps the sequence's review tag. Re-reads first so tags changed elsewhere survive; returns the tags written. */
export async function setReview(seqId: string, state: Review): Promise<string[]> {
  const data = (await client()).dataClient;
  const tags = withReview((await data.getSequence(seqId)).sequenceTags, state);
  await data.updateSequence(seqId, undefined, tags, undefined, undefined, { paths: ['sequence_tags'] });
  return tags;
}
```

The `ViamClient`, `RobotClient`, `Sequence` and `Credentials` type exports were verified against SDK 0.76. Don't edit `auth.js`.

- [ ] **Step 2: Check**

Run: `npm run check && npm test`
Expected: 0 errors, and the tests still pass.

- [ ] **Step 3: Commit**

```bash
git add src/data.ts
git commit -m "feat: data layer for episode review (list, frames, bytes, review tags)"
```

---

### Task 4: `Review.svelte`: episode list, polling, tagging, keys, player

**Files:**
- Create: `src/Review.svelte`

Read the spec's `Review.svelte` section first. The component below implements it as written. Svelte 5 points to respect:

- **Effects re-run on synchronous reads.** An `$effect` re-runs when any state it reads *synchronously* changes. Reads after an `await` are not tracked. Wrap synchronous reads you don't want tracked in `untrack(...)`.
- **The episode-load effect keys on `currentId`.** `currentId` is a `$derived` string. Keying on the id, not the `Sequence` object, stops a list re-poll, which creates new objects for the same episodes, from restarting playback.
- **Stale results.** Only an episode change runs `++loadGen`. Every async result checks `gen === loadGen` before writing.

- [ ] **Step 1: Create the component**

Create `src/Review.svelte`:

```svelte
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
        error = '';
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
          <span class="n">#{session ? i + 1 : rows.length - i}</span>
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
```

If svelte-check flags something, fix it with the smallest change that keeps the behavior, and record the change in your report. For example:
- **The `blurAfter` handler type.** If the `onclick` type doesn't match, inline the handler instead.
- **The `shown` double-`map`.** Collapse it into one `map` if you like: get the loaded frame, then look up its URL.

- [ ] **Step 2: Check, test, build**

Run: `npm run check && npm test && npm run build`
Expected: 0 errors and 0 warnings, all tests pass, and the build succeeds. The component isn't mounted yet; Task 5 wires it in.

- [ ] **Step 3: Commit**

```bash
git add src/Review.svelte
git commit -m "feat(ui): Review component: episode queue/history, synced player, keep/discard tagging"
```

---

### Task 5: Wire Review into the Recorder

**Files:**
- Modify: `src/Recorder.svelte`

- [ ] **Step 1: State and import**

Add the import next to the `CameraFeed` import:

```ts
  import Review from './Review.svelte';
```

Below `let showKeys = $state(false);`, add:

```ts
  // Episode review (spec B): replaces the cameras and drawer while open; only when no session is active.
  let reviewing = $state<{ session?: { id: string; expected: number } } | undefined>();
```

Below `const canStart = …`, add:

```ts
  const episodesDone = $derived(session ? Object.values(session.counts).reduce((a, b) => a + b, 0) : 0);
  const canReview = $derived(connected && !!robot.current && !active && !homing);
  function openReview(queue: boolean) {
    if (!canReview) return;
    reviewing = { session: queue && session && episodesDone > 0 ? { id: session.id, expected: episodesDone } : undefined };
  }
```

- [ ] **Step 2: Keys**

In `onKey`, add this line directly after the first line (`if (e.repeat || e.ctrlKey || e.metaKey || e.altKey) return;`):

```ts
    if (reviewing && e.key !== 't' && e.key !== 'T') return; // Review owns the keys; T (teleop / emergency stop) still works
```

- [ ] **Step 3: A banners snippet**

Move the three banner blocks out of the drawer into a snippet, defined next to `robotControls`. The three blocks are:
- the `error` banner;
- the `orphaned` banner with its Stop button;
- the "Teleop stopped itself" banner.

```svelte
  {#snippet banners()}
    {#if error}<p class="banner error" role="alert">{error}</p>{/if}
    {#if orphaned}
      <p class="banner warn" role="alert">
        Capture is still running on <strong>{controllerName}</strong>.
        <button onclick={stopOrphan}>Stop</button>
      </p>
    {/if}
    {#if session?.phase === 'recording' && teleopMode === 'error'}
      <p class="banner warn" role="alert">Teleop stopped itself: {teleopStatus?.last_error}. Space ends the episode.</p>
    {/if}
  {/snippet}
```

At the top of the drawer, where those blocks were, put `{@render banners()}`.

- [ ] **Step 4: Swap in Review**

Change the `<main>` opening tag to `<main class:reviewing={!!reviewing}>`.

Wrap the existing `<section class="cams">…</section>` **and** `<section class="drawer">…</section>` in an if-block:

```svelte
  {#if reviewing && robot.current}
    <div class="review-banners">{@render banners()}</div>
    <Review robot={robot.current} session={reviewing.session} onclose={() => (reviewing = undefined)} />
  {:else}
    <section class="cams …">…unchanged…</section>
    <section class="drawer">…unchanged…</section>
  {/if}
```

Leave the shortcut overlay (`{#if showKeys}`) after the if-block, unchanged.

- [ ] **Step 5: Entry buttons**

In the drawer's idle/done branch (the `{:else}` that holds the palette and form), replace the existing done paragraph with the version below. It keeps the paragraph and adds a row of buttons:

```svelte
      {#if session}
        <p class="last">
          <strong class="done">DONE</strong> · {session.episode} / {session.settings.episodes} episodes
        </p>
      {/if}
      <div class="row">
        {#if session && episodesDone > 0}
          <button type="button" disabled={!canReview} onclick={(e) => { openReview(true); e.currentTarget.blur(); }}>
            Review session ({episodesDone} episodes)
          </button>
        {/if}
        <button type="button" disabled={!canReview} onclick={(e) => { openReview(false); e.currentTarget.blur(); }}>Episodes</button>
      </div>
```

- [ ] **Step 6: Styles**

Add `main.reviewing { grid-template-rows: auto minmax(0, 1fr); }` and `.review-banners { display: grid; gap: 8px; padding: 0 16px; }`. If the drawer CSS doesn't already have a `.row` rule, add `.row { display: flex; flex-wrap: wrap; gap: 8px; }`.

- [ ] **Step 7: Check, test, build**

Run: `npm run check && npm test && npm run build`
Expected: 0 errors and 0 warnings from check, all tests pass, and the build succeeds. Vite will warn that a chunk is over 500 kB, because `createViamClient` pulls in the app clients. That warning is expected: ignore it, and don't code-split.

- [ ] **Step 8: Commit**

```bash
git add src/Recorder.svelte
git commit -m "feat(ui): open episode review from the done/idle drawer; banners stay visible"
```

---

### Task 6: README

**Files:**
- Modify: `README.md`

- [ ] **Step 1:** After the `### Keyboard` section, add:

````markdown
### Reviewing episodes

When a session ends, **Review session** lists that session's episodes as they sync. Each episode plays its camera frames in real time. **K** tags the episode `review:keep` and **D** tags it `review:discard`. Each one then jumps to the next episode you haven't reviewed. Nothing is deleted; untagged episodes count as not reviewed.

**Episodes** opens the same player for this machine's past episodes, with filters for task, session and review status.

| Key | Action |
|---|---|
| K / D | Keep / discard, then go to the next unreviewed episode |
| ← / → | Previous / next episode |
| Space | Play / pause |
| , / . | Step one frame |
| Esc | Close review |

Review reads and tags data with the machine's own credentials. A sequence can appear before all its frames have synced. Keep and Discard unlock once the frames have caught up, or once no new frames arrive for 15 s, in which case the episode is marked partial.
````

- [ ] **Step 2: Commit**

```bash
git add README.md
git commit -m "docs: README section for episode review"
```

---

### Task 7: End-to-end on pi-arm6 (operator, by hand)

- [ ] **Step 1:** Run `npm run build`, then deploy the build with the local server or a module upload.
- [ ] **Step 2:** Open **Episodes** and select `pick up the pen`. Play it, scrub, and step with `,` and `.`.
- [ ] **Step 3:** Press K, then select the episode again and press D. In the Viam app, the sequence's tags should be the original three plus `review:discard` only.
- [ ] **Step 4:** Record a 2-episode session, then click **Review session (2 episodes)**.
  - Episode 2 should go from "⟳ syncing…" to "syncing frames (x)" to playable.
  - Tag both episodes. K and D should jump to the next unreviewed episode.
- [ ] **Step 5:** Press Esc. The live cameras should come back within about 5 s.
- [ ] **Step 6:** Run a ponytail audit (`/ponytail-audit`) before merging.
