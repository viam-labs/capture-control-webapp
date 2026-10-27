# capture-control-webapp v2 — Episode Review Design (spec B)

This feature lets the operator review a session's episodes after the session ends, tagging each one keep or discard. It also lets them browse and retag past episodes. Spec A (operator loop) is already merged. Spec C (session notes) is deferred.

Wireframe: https://claude.ai/artifact/3bMbz9zoJeusUyfPV8gMUd

## Goals

1. **Review queue (primary).** When a session ends, list that session's episodes as they sync. Play each one and tag it keep or discard.
2. **History (secondary).** Browse this machine's past episodes, filter them by task, session, and review status, then play and retag them.

## Non-goals

- Joint traces or charts in the player.
- Datasets, export, and training.
- Deleting sequences or data.
- Instant local replay from the live feeds.
- Reviewing while a session is running.
- Other machines' episodes.
- Server-side pagination of the history.

## Decisions

| Topic | Decision |
|---|---|
| When to review | An **end-of-session review queue**. It never blocks the capture loop, and episodes appear as they sync. |
| Discard | **Tag only.** `updateSequence` adds `review:keep` or `review:discard` and replaces any earlier `review:*` tag. Nothing is deleted. An untagged episode counts as not reviewed. |
| Player | **Synced camera playback.** Cameras play side by side in real time, with play/pause, a scrubber, and frame stepping. There is no joint data. |
| After K or D | Jump to the **next unreviewed** episode. |
| Layout | Review **replaces** the live cameras and the drawer's controls. The error and orphan banners stay visible above it. Esc brings the live view back. |
| Data access | The browser calls the Data API directly, using the machine's existing cookie credentials. There is no module proxy and no extra key. |

### Verified, and the gate

On 2026-10-26, a probe ran against pi-arm6 with an Owner-role, machine-scoped API key. Every read succeeded:

- `listSequences(orgId)`: org-wide; the app filters by `partId`.
- `getSequence`
- `getSequenceBinaryData`: returns metadata only; `binary` is empty.
- `binaryDataByIds(ids, true)`: returns the bytes, about 38 KB per JPEG.
- `tabularDataByFilter`

Implementation task 1 then ran the same probe with the real viamapplications.com cookie key, served through `viam module local-app-testing`. Every read passed, and so did `updateSequence`: it added `review:probe` and then restored the original tags.

If a future key is denied reads or writes, stop and come back to the Data access decision. Don't build a proxy or a fallback without a spec.

## Units

```text
src/
├── review.ts        # pure, no SDK: tags, timeline, stepping, navigation
├── review.test.ts
├── data.ts          # thin Data API layer
├── Review.svelte    # player + episode list (queue and history)
└── Recorder.svelte  # entry points; swaps <Review> in while open
```

### `review.ts` (pure)

Frame times are **epoch milliseconds** (`number`) throughout.

- `parseTags(tags)` returns `{ task, session, review }`. `review` is `'keep' | 'discard' | undefined`.
- `withReview(tags, state)` returns a copy of `tags`. Every `review:*` tag is removed and `review:<state>` is appended. The other tags keep their order.
- `timeline(frames)` takes `{ id, camera, time }[]` and returns `Map<camera, frame[]>`, with each list sorted by `time`.
- `frameAt(frames, t)` returns the last frame with `time <= t`, or `undefined` before the first frame. It uses binary search.
- `step(tl, t, dir)` returns the next (`dir = 1`) or previous (`dir = -1`) **distinct frame time across all cameras**, strictly after or before `t`. If there is none, it returns `t`.
- `span(tl)` returns `{ start, end }`, the minimum and maximum frame times.
- `nextUnreviewed(episodes, from)` returns the index of the next episode after `from` that is **listed** (not a "syncing…" placeholder) and has no `review`. It wraps around, and returns `-1` if there is none. Playability is not part of this. It only gates K and D on the selected episode.

### `data.ts` (thin SDK layer, not unit tested)

- `client()` calls `createViamClient({ credentials })` lazily, with `credentials` from `auth.js` `getHostAndCredentials()`. `serviceHost` defaults to app.viam.com.
  - **Cache the promise, not the resolved client,** so concurrent first calls share one transport.
  - **Clear the cached promise if it rejects,** so Retry can recover.
- `ids(robot)` calls `robot.getCloudMetadata()` and returns `{ orgId: primaryOrgId, partId: machinePartId }`. Its promise is cached and cleared on rejection, the same way as `client()`.
- `listEpisodes({ orgId, partId, session? })` pages through `listSequences(orgId)`. It keeps the sequences whose `partId` matches and, when `session` is given, that carry the tag `session:<id>`. Results are sorted by `startTime`.
  - `ponytail:` it scans the whole org on every call. If the org grows, add a start-time cutoff or cache the pages already seen.
- `frameIndex(seqId)` pages through `getSequenceBinaryData`. It keeps only the items where `d.metadata.captureMetadata.mimeType` starts with `image/`, and maps each one to:
  ```ts
  {
    id: d.metadata.binaryDataId,
    camera: d.metadata.captureMetadata.componentName,
    mime: d.metadata.captureMetadata.mimeType,
    time: (d.metadata.timeRequested ?? d.metadata.timeReceived)!.toDate().getTime(),
  }
  ```
- `frameBytes(frames)` takes at most 20 frames per call. It calls `binaryDataByIds(ids, true)` and returns a `Map<id, Blob>` keyed by `d.metadata.binaryDataId`, because the result order isn't guaranteed. Each blob is `new Blob([d.binary], { type: mime })`. **No object URLs are created here.**
- `setReview(seqId, state)` makes sure tags changed elsewhere aren't lost:
  1. Re-reads the sequence with `getSequence`.
  2. Computes `tags = withReview(fresh.sequenceTags, state)`.
  3. Calls `updateSequence(seqId, undefined, tags, undefined, undefined, { paths: ['sequence_tags'] })`.
  4. Returns `tags`.

### `Review.svelte`

**Props:** `{ robot, session?: { id: string; expected: number }, onclose }`.

**Queue mode (`session` given)**

- Polls `listEpisodes` with a **`setTimeout` chain**. The next poll is scheduled 5 s after the previous one finishes, so polls never overlap. The chain is cleared in the `$effect` cleanup.
- Polling stops once `expected` sequences have been found.
- Rows for episodes that haven't appeared yet show "syncing…".

**History mode (no `session`)**

- Lists all of this machine's episodes, newest first.
- Filters by task, session, and review status, all client-side.
- Renders the full filtered list, with no "load more".

**Playable**

A listed sequence can still be missing frames, because JPEGs sync separately from the `.seq` file. The selected episode is **playable** in either of these cases:

- Its last frame time is at least `endTime − 1 s`, where `endTime` is `sequence.endTime.toDate().getTime()`.
- Its frame count is above 0 and hasn't changed across 3 consecutive re-fetches (15 s). The episode is then marked "partial".

An episode with 0 frames is never playable.

Until the episode is fully synced:

- The player re-fetches `frameIndex` every 5 s, using the same `setTimeout`-chain pattern. The re-fetches **keep running while the episode is partial**, so a slow sync catches up on its own and the "partial" label clears. They stop once the first condition holds.
- It shows "syncing frames (x)".
- **K and D are disabled** until the episode is playable.

**Player**

- **Playhead.** It runs from `span.start` to `span.end` in epoch ms.
- **Playback.** Playing advances the playhead by real elapsed time, using the `requestAnimationFrame` delta. Each camera shows `frameAt(cameraFrames, playhead)`. If that frame's bytes haven't loaded yet, the camera keeps its last loaded frame.
- **Stepping.** `,` and `.` call `step()`.
- **Loading.** Frame bytes are fetched from the playhead forward, in batches of 20, until **the whole episode is loaded**. Object URLs are created here from the returned blobs. All of them are revoked when the episode changes and on unmount.
  - `ponytail:` it keeps every frame of the episode, about 34 MB for 900 frames. If long episodes become a problem, add a window around the playhead.
- **Scrubber.** An `<input type="range">` over the span. Behind it, a band shows which frames have loaded.
- **Stale results.** Only an episode change runs `++loadGen`. Every async load (frame index, byte batches, re-fetches) captures `const gen = loadGen` and drops its result if `gen !== loadGen`. A batch left over from a previous episode therefore never creates object URLs, and a stale index never replaces the timeline. Loads for the same episode don't cancel each other.

**Keys**

Review owns these while it is open:

| Key | Action |
|---|---|
| K / D | Keep / discard (only when playable), then go to the next unreviewed episode |
| ← / → | Previous / next episode |
| Space | Play / pause |
| `,` / `.` | Step to the previous / next frame time |
| Esc | Close |

- Space and the arrow keys call `preventDefault`.
- Keys are ignored on repeat and when Ctrl, Meta, or Alt is held.
- Keys are ignored while focus is in a text input, select, or textarea. **The range scrubber is exempt.** It is also blurred on `change`, so the arrow keys don't move it natively.
- Every button blurs itself after a click (`e.currentTarget.blur()`), as Recorder's buttons already do. This stops Space from activating a focused button.
- **After a successful tag,** the row's tags are replaced with the tags `setReview` returned, before `nextUnreviewed` runs. A failed tag leaves the row as it was.

### `Recorder.svelte`

**Buttons**

- **Done drawer:** add **Review session (N episodes)**, where `N = Object.values(session.counts).reduce((a, b) => a + b, 0)`. Hide the button when `N` is 0.
- Both the done and idle drawers get an **Episodes** button.

**Opening Review**

- Allowed only when no session is active (neither recording nor reset) and `!homing`.
- Pass `session={{ id: session.id, expected: N }}` for the queue. Pass nothing for the history.

**While Review is open**

- `<Review>` replaces the camera grid and the drawer's controls.
- **The error and orphan banners stay rendered above it.** Their Stop button keeps working.
- `onKey` returns early for every key except **T**, which keeps working as the teleop toggle and emergency stop.
- The command driver keeps running. It has nothing to do while no session is active.
- On close, the live cameras come back. `CameraFeed`'s watchdog re-establishes the streams within about 1–5 s.

## Failure handling

| Case | Behavior |
|---|---|
| Read denied | The Review screen shows "This machine's key can't read captured data" with the error text. Capture is unaffected. |
| `updateSequence` denied | Show an error banner. The row's tag doesn't change, and playback keeps working. |
| An episode never appears | Its row stays "syncing…" while the queue is open. Polling stops on close. |
| Frames never finish syncing | After 15 s with no new frames (and at least one frame), the episode becomes playable and is marked "partial". Re-fetching continues, so the label clears if the rest arrives. |
| A frame batch fails | Retry it twice. Then mark the gap on the scrubber and keep playing the frames that did load. |
| A cloud call fails mid-review | Show a banner with **Retry**. Tags already written stay written. Cached client/ids promises are cleared, so a retry starts fresh. |
| A tag write races a retag elsewhere | Last write wins. `setReview` re-reads the tags first, so the other tags survive. |
| Capture is still running (orphan) | The orphan banner stays visible above Review, with its Stop button. |

## Testing

`src/review.test.ts` (node:test) covers:

- `parseTags`: all fields, and a missing `review`.
- `withReview`: replaces an earlier `review:*` tag, and keeps the other tags in order.
- `timeline` / `frameAt`: before the first frame, between frames, exactly on a frame, and past the end, with two cameras whose timestamps are offset.
- `step`: the next and previous distinct times across the offset cameras, and the ends.
- `span`.
- `nextUnreviewed`: wraps around, skips placeholders and episodes that are already reviewed, and returns `-1` when none are left.

Manual end-to-end check on pi-arm6:

1. Open Episodes and play the existing `pick up the pen` sequence.
2. Tag it keep, then discard. In the Viam app, confirm that only the `review:` tag changed.
3. Record a two-episode session and open the review queue. Watch episode 2 go from "syncing…" to "syncing frames" to playable, then tag both episodes.
