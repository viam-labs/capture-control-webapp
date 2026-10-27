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
