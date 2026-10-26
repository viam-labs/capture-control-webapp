import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cap from './capture.ts';

const settings: cap.Settings = { episodes: 2, episodeS: 10, resetS: 5, fps: 15 };
const START = { start_capture: true, frequency_hz: 15, tags: ['task:pick cube', 'session:abc', 'fps:15'] };
const STOP = { stop_capture: true };

test('full run: record, reset, record, done; no reset after the last episode', () => {
  let [s, cmds] = cap.start(settings, 'pick cube', 'abc', 0);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.phase, 'recording');
  assert.equal(s.episode, 1);

  [s, cmds] = cap.tick(s, 9_999);
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'recording');

  [s, cmds] = cap.tick(s, 10_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'reset');

  [s, cmds] = cap.tick(s, 15_000);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.phase, 'recording');
  assert.equal(s.episode, 2);

  [s, cmds] = cap.tick(s, 25_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'done');

  [s, cmds] = cap.tick(s, 99_000);
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'done');
});

test('next() ends recording and reset early, restarting the timer from now', () => {
  let [s] = cap.start(settings, 'pick cube', 'abc', 0);
  let cmds: cap.Command[];

  [s, cmds] = cap.next(s, 3_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'reset');
  assert.equal(cap.remainingMs(s, 3_000), 5_000);

  [s, cmds] = cap.next(s, 4_000);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.episode, 2);
  assert.equal(cap.remainingMs(s, 4_000), 10_000);
});

test('stop() while recording sends stop_capture; during reset it sends nothing', () => {
  let [s] = cap.start(settings, 'pick cube', 'abc', 0);
  let [stopped, cmds] = cap.stop(s);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(stopped.phase, 'done');

  [s] = cap.next(s, 1_000); // now in reset
  [stopped, cmds] = cap.stop(s);
  assert.deepEqual(cmds, []);
  assert.equal(stopped.phase, 'done');
});

test('controller readings helpers', () => {
  const r = {
    overrides: [
      { resource_name: 'arm', method: 'JointPositions', capture_frequency_hz: 0 },
      { resource_name: 'wrist-cam', method: 'GetImages', capture_frequency_hz: 0 },
    ],
    sequences: [],
  };
  assert.equal(cap.isController(r), true);
  assert.equal(cap.isController({ temperature: 20 }), false);
  assert.deepEqual(cap.controlledResources(r), ['arm', 'wrist-cam']);
  assert.equal(cap.hasOpenSequence(r), false);
  assert.equal(cap.hasOpenSequence({ ...r, sequences: [{ resources: [], sequence_tags: [] }] }), true);
});

test('open-ended episode (episodeS null) runs until next(); reset stays timed', () => {
  const open: cap.Settings = { ...settings, episodeS: null };
  let [s] = cap.start(open, 'pick cube', 'abc', 0);
  let cmds: cap.Command[];

  [s, cmds] = cap.tick(s, 1e9);
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'recording');
  assert.equal(cap.remainingMs(s, 1e9), Infinity);
  assert.equal(cap.elapsedMs(s, 7_000), 7_000);

  [s, cmds] = cap.next(s, 8_000);
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.phase, 'reset');
  assert.equal(cap.remainingMs(s, 8_000), 5_000);
  assert.equal(cap.elapsedMs(s, 9_000), 1_000);

  [s, cmds] = cap.tick(s, 13_000);
  assert.deepEqual(cmds, [START]);
  assert.equal(s.phase, 'recording');
  assert.equal(s.episode, 2);
  assert.equal(cap.remainingMs(s, 13_000), Infinity);
});

test('the task passed when an episode starts tags it; counts per task', () => {
  let [s] = cap.start(settings, 'reach', 'abc', 0);
  let cmds: cap.Command[];

  [s, cmds] = cap.next(s, 1_000, 'grasp'); // ends 'reach'; the pick only matters when an episode starts
  assert.deepEqual(cmds, [STOP]);
  assert.equal(s.episodeTask, 'reach');
  assert.deepEqual(s.counts, { reach: 1 });

  [s, cmds] = cap.tick(s, 6_000, 'grasp'); // reset expires
  assert.deepEqual(cmds, [{ start_capture: true, frequency_hz: 15, tags: ['task:grasp', 'session:abc', 'fps:15'] }]);
  assert.equal(s.episodeTask, 'grasp');

  [s] = cap.next(s, 7_000, 'place'); // last of 2 episodes → done
  assert.equal(s.phase, 'done');
  assert.deepEqual(s.counts, { reach: 1, grasp: 1 });
});

test('stop() counts the open episode; stop() during reset does not', () => {
  let [s] = cap.start(settings, 'reach', 'abc', 0);
  assert.deepEqual(cap.stop(s)[0].counts, { reach: 1 });

  [s] = cap.next(s, 1_000); // reset; reach counted once
  assert.deepEqual(cap.stop(s)[0].counts, { reach: 1 });
});

test('hold() keeps the reset open until next(); no-op while recording', () => {
  let [s] = cap.start(settings, 'reach', 'abc', 0);
  assert.equal(cap.hold(s), s);

  [s] = cap.next(s, 1_000);
  s = cap.hold(s);
  let cmds: cap.Command[];
  [s, cmds] = cap.tick(s, 1e9, 'grasp');
  assert.deepEqual(cmds, []);
  assert.equal(s.phase, 'reset');
  assert.equal(cap.remainingMs(s, 1e9), Infinity);

  [s, cmds] = cap.next(s, 1e9, 'grasp');
  assert.equal(s.phase, 'recording');
  assert.equal(s.episodeTask, 'grasp');
});
