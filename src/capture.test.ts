import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as cap from './capture.ts';

const settings: cap.Settings = { task: 'pick cube', episodes: 2, episodeS: 10, resetS: 5, fps: 15 };
const START = { start_capture: true, frequency_hz: 15, tags: ['task:pick cube', 'session:abc', 'fps:15'] };
const STOP = { stop_capture: true };

test('full run: record, reset, record, done; no reset after the last episode', () => {
  let [s, cmds] = cap.start(settings, 'abc', 0);
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
  let [s] = cap.start(settings, 'abc', 0);
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
  let [s] = cap.start(settings, 'abc', 0);
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
  let [s] = cap.start(open, 'abc', 0);
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
