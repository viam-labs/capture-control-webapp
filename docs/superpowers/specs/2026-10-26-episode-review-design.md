# capture-control-webapp v2: Episode Review Design (spec B)

When a recording session ends, the operator should be able to review its episodes and tag each one keep or discard. They should also be able to browse and retag past episodes. Spec A, the operator loop, is merged. Spec C, session notes, is deferred.

Wireframe: https://claude.ai/artifact/3bMbz9zoJeusUyfPV8gMUd

## Goals

1. **Review queue (primary).** When a session ends, list that session's episodes as they sync, play each one, and tag it keep or discard.
2. **History (secondary).** Browse this machine's past episodes, filter them by task, session and review status, play them, and retag them.

## Non-goals

- Joint traces or charts in the player.
- Datasets, export, and training.
- Deleting sequences or data.
- Instant local replay from the browser's live feeds.
- Review during a session (the queue opens only after the session ends).
- Reviewing other machines' episodes.

## Decisions

| Topic | Decision |
|---|---|
| When to review | The **end-of-session review queue**. It doesn't block the capture loop, and episodes appear as they sync. |
| Discard | **Tagging.** `updateSequence` adds `review:keep` or `review:discard` and replaces any earlier `review:*` tag. Nothing is deleted. An untagged episode counts as not reviewed. |
| Player | **Synced camera playback.** Cameras play side by side at the episode's `fps:` tag, with play, pause, a scrubber and frame stepping. There is no joint data. |
| After K or D | Jump to the **next unreviewed** episode. |
| Layout | Review **replaces** the live cameras and the drawer while it's open. Esc brings them back. |
| Data access | The browser calls the Data API directly with the machine's existing cookie credentials. There's no module proxy and no extra key. |

### Verified: the machine-scoped key can read

On 2026-10-26 a probe ran against pi-arm6 using an Owner-role, machine-scoped API key. Every read succeeded:

- `listSequences(orgId)` (org-wide; the app filters by `partId`)
- `getSequence`
- `getSequenceBinaryData` (metadata only; `binary` is empty)
- `binaryDataByIds(ids, true)` (the bytes; about 38 KB per JPEG)
- `tabularDataByFilter`

Two things are still unverified:

- **Write access.** `updateSequence` was not tested.
- **Cookie key role.** The viamapplications.com cookie key may have the Operator role.

Implementation task 1 checks both using the real cookie key.

## Units

```text
src/
├── review.ts        # pure, no SDK: tags, timeline, navigation
├── review.test.ts
├── data.ts          # thin Data API layer
├── Review.svelte    # player + episode list (queue and history)
└── Recorder.svelte  # entry points; swaps <Review> in while open
```

### `review.ts` (pure)

- **`parseTags(tags)`** returns `{ task, session, fps, review }`.
  - `review` is `'keep'`, `'discard'` or `undefined`.
  - `fps` is a number. If it's missing, fall back to 10.
- **`withReview(tags, state)`** returns a copy of `tags` with every `review:*` tag removed and `review:<state>` appended. It leaves the other tags in their original order.
- **`timeline(frames)`** takes frame metadata (`{ id, camera, time }`) and returns `Map<camera, frame[]>`, with each list sorted by `time`.
- **`frameAt(frames, t)`** returns the last frame with `time <= t`, or `undefined` before the first frame. It uses binary search.
- **`nextUnreviewed(episodes, from)`** returns the index of the next episode after `from` that is synced and has no `review` tag. It wraps around and returns `-1` when there is none.

### `data.ts` (thin SDK layer, not unit tested)

- **`client()`**: lazily calls `createViamClient({ credentials })` with the credentials from `auth.js` `getHostAndCredentials()`, then caches the client.
- **`ids(robot)`**: calls `robot.getCloudMetadata()` and returns `{ orgId: primaryOrgId, partId: machinePartId }`, cached.
- **`listEpisodes({ orgId, partId, session? })`**: pages through `listSequences(orgId)` and keeps the sequences whose `partId` matches and, when `session` is set, that carry the tag `session:<id>`. It returns them sorted by `startTime`.
  - `ponytail:` this scans the whole org on every call. If the org grows, add a start-time cutoff or cache the pages already seen.
- **`frameIndex(seqId)`**: pages through `getSequenceBinaryData` and maps each item to `{ id: binaryDataId, camera: captureMetadata.componentName, time: timeRequested ?? timeReceived }`.
- **`frameBytes(ids)`**: calls `binaryDataByIds(ids, true)` and returns `Map<id, objectURL>`. The caller passes at most 20 ids per call.
- **`setReview(seqId, state)`**:
  1. Re-read the sequence with `getSequence`, so tags changed elsewhere aren't overwritten.
  2. Call `updateSequence(seqId, undefined, withReview(tags, state), undefined, undefined, { paths: ['sequence_tags'] })`.

### `Review.svelte`

- **Props:** `{ robot, session?: { id: string; episodes: number }, onclose }`.
- **Queue mode (`session` set):**
  - Polls `listEpisodes` every 5 s until it has found `session.episodes` sequences, or until the view closes.
  - Rows for episodes that haven't appeared yet show "syncing…". The app knows the count from the session (`Object.values(counts)` summed).
- **History mode (no `session`):**
  - Lists all of this machine's episodes, newest first.
  - Filters by task, session and review status.
  - Loads 50 rows at a time, with **Load more**.
- **Player:**
  - For the selected episode, loads `frameIndex`, then fetches bytes from the playhead forward in batches of 20.
  - Plays at the `fps:` rate. Each camera shows `frameAt(cameraFrames, playhead)`.
  - If the next frame hasn't loaded, it holds the last loaded frame.
  - The scrubber shows a band for the frames that have loaded.
  - Keeps at most about 300 object URLs and revokes the oldest beyond that (`ponytail:` cap). Revokes them all when the episode changes and on unmount.
- **Keys** (owned by Review while it's open):

  | Key | Action |
  |---|---|
  | K / D | Keep / discard, then go to the next unreviewed episode |
  | ← / → | Previous / next episode |
  | Space | Play / pause |
  | `,` / `.` | Step one frame |
  | Esc | Close |

  Keys are ignored when focus is in an input, select or textarea, on repeat, and when Ctrl, Meta or Alt is held. These are the same guards Recorder uses.

### `Recorder.svelte`

- **Done drawer:** add **Review session (N episodes)** and **Episodes**.
- **Idle drawer:** add **Episodes**.
- **Opening review:** only possible when no session is active (no recording and no reset).
- **While review is open:**
  - Render `<Review>` in place of the camera grid and the drawer.
  - `onKey` returns early.
  - The command driver keeps running. It has nothing to do while no session is active.
- **On close:** the live cameras come back. `CameraFeed`'s watchdog re-establishes the streams.

## Failure handling

| Case | Behavior |
|---|---|
| Read denied | The Review screen shows "This machine's key can't read captured data" and the error text. The capture loop is unaffected. |
| `updateSequence` denied | Show an error banner. The tag doesn't change, and playback keeps working. |
| An episode never appears | Its row stays at "syncing…" while the queue is open. Polling stops on close. |
| A frame batch fails | Retry it twice, then mark the gap on the scrubber and keep playing the frames that did load. |
| Cloud call fails mid-review | Show a banner with **Retry**. Tags already written stay written. |
| Tag races a retag elsewhere | Last write wins. `setReview` re-reads tags first, so other tags survive. |

## Testing

`src/review.test.ts` (node:test) covers:

- **`parseTags`:** every field, plus a missing `fps`.
- **`withReview`:** replaces an earlier `review:*` tag and keeps the other tags in order.
- **`timeline` / `frameAt`:**
  - before the first frame, between frames, exactly on a frame, and past the end;
  - two cameras with offset timestamps.
- **`nextUnreviewed`:** wraps around, skips episodes that are syncing or already reviewed, and returns `-1` when none are left.

Manual end-to-end on pi-arm6:

1. Open Episodes and play the existing `pick up the pen` sequence.
2. Tag it keep, then discard. In the Viam app, confirm that only the `review:` tag changed.
3. Record a two-episode session and open the review queue. Watch episode 2 go from syncing to playable, then tag both episodes.
