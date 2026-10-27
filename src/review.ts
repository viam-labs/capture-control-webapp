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
