import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as r from './runner.ts';

const seg = (session: string, episode: number, source: 'policy' | 'correction', outcome: r.Outcome): r.Segment => ({
  session, episode, source, task: 't', checkpoint: 'c', t_start: '', t_end: '', outcome, error: '', captured: outcome !== 'error',
});

test('asRunnerStatus accepts a runner reply, rejects executor, teleop and junk', () => {
  const ok = { episode_runner: true, state: 'idle', session: null, episode: null, last_error: '' };
  assert.equal(r.asRunnerStatus(ok), ok);
  assert.equal(r.asRunnerStatus({ state: 'running', last_error: '' }), undefined);
  assert.equal(r.asRunnerStatus({ running: true }), undefined);
  assert.equal(r.asRunnerStatus(null), undefined);
});

test('outcomeFor matches session, episode and source', () => {
  const segs = [seg('s1', 2, 'policy', 'success'), seg('s1', 2, 'correction', 'takeover')];
  const tags = ['session:s1', 'episode:2', 'source:policy'];
  assert.equal(r.outcomeFor(tags, segs), 'success');
  assert.equal(r.outcomeFor(['session:s1', 'episode:2', 'source:correction'], segs), 'takeover');
  assert.equal(r.outcomeFor(['session:s1', 'episode:3', 'source:policy'], segs), undefined);
  assert.equal(r.outcomeFor(['session:s2', 'episode:2', 'source:policy'], segs), undefined);
});

test('withOutcome replaces an existing outcome tag', () => {
  assert.deepEqual(r.withOutcome(['task:x', 'outcome:failure', 'session:s'], 'success'), ['task:x', 'session:s', 'outcome:success']);
});

test('summary counts and rates over success, failure and takeover only', () => {
  const segs = [
    seg('s', 1, 'policy', 'success'), seg('s', 2, 'policy', 'success'), seg('s', 3, 'policy', 'failure'),
    seg('s', 4, 'policy', 'takeover'), seg('s', 4, 'correction', 'success'), seg('s', 5, 'policy', 'error'), seg('s', 6, 'policy', 'stopped'),
  ];
  assert.deepEqual(r.summary(segs), { episodes: 6, success: 2, failure: 1, takeover: 1, error: 1, stopped: 1, successRate: 0.5, corrections: 1 });
  assert.equal(r.summary([seg('s', 1, 'policy', 'error')]).successRate, undefined);
});

test('needsHeartbeat and isIdle: unknown states are busy and need no heartbeat', () => {
  for (const s of ['starting', 'policy', 'correction']) assert.ok(r.needsHeartbeat(s) && !r.isIdle(s));
  for (const s of ['idle', 'no_session']) assert.ok(r.isIdle(s) && !r.needsHeartbeat(s));
  assert.ok(!r.isIdle('weird') && !r.needsHeartbeat('weird'));
});

test('capturedCount counts only segments that started a capture', () => {
  assert.equal(r.capturedCount([seg('s', 1, 'policy', 'success'), seg('s', 2, 'policy', 'error'), seg('s', 2, 'correction', 'success')]), 2);
});

test('pendingOutcomes skips tagged, unmatched and already-written sequences', () => {
  const segs = [seg('s', 1, 'policy', 'takeover'), seg('s', 1, 'correction', 'success'), seg('s', 2, 'policy', 'failure')];
  const tags = (e: number, src: string, extra: string[] = []) => [`session:s`, `episode:${e}`, `source:${src}`, ...extra];
  const seqs = [
    { id: 'a', tags: tags(1, 'policy') },
    { id: 'b', tags: tags(1, 'correction', ['outcome:success']) }, // already right
    { id: 'c', tags: tags(2, 'policy', ['outcome:success']) }, // wrong: replaced
    { id: 'd', tags: tags(9, 'policy') }, // not in the log
    { id: 'e', tags: tags(2, 'policy') }, // written already
  ];
  assert.deepEqual(r.pendingOutcomes(seqs, segs, new Set(['e:failure'])), [
    { id: 'a', outcome: 'takeover' },
    { id: 'c', outcome: 'failure' },
  ]);
});
