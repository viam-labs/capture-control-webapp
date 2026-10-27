import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as rb from './robot.ts';

function fakeTeleop(log: string[], failStop = false): rb.DoCommander {
  return {
    async doCommand(cmd) {
      log.push(`teleop:${cmd.command}`);
      if (failStop && cmd.command === 'stop') throw new Error('unreachable');
      return { running: false };
    },
  };
}

const saver = (name: string, log: string[], behavior: 'ok' | 'fail' | 'hang' = 'ok'): rb.Saver => ({
  name,
  setPosition(p) {
    log.push(`${name}:${p}`);
    if (behavior === 'hang') return new Promise(() => {});
    return behavior === 'fail' ? Promise.reject(new Error('stall')) : Promise.resolve();
  },
});

test('home stops teleop, moves every saver, and leaves teleop off', async () => {
  const log: string[] = [];
  await rb.home(fakeTeleop(log), [saver('leader-home', log), saver('follower-home', log)]);
  assert.deepEqual(log, ['teleop:stop', 'leader-home:2', 'follower-home:2']);
});

test('home without a teleop service just moves', async () => {
  const log: string[] = [];
  await rb.home(undefined, [saver('leader-home', log)]);
  assert.deepEqual(log, ['leader-home:2']);
});

test('a failed teleop stop moves nothing', async () => {
  const log: string[] = [];
  await assert.rejects(rb.home(fakeTeleop(log, true), [saver('leader-home', log)]), /unreachable/);
  assert.deepEqual(log, ['teleop:stop']);
});

test('a failed move names the saver after every move settles', async () => {
  const log: string[] = [];
  await assert.rejects(
    rb.home(undefined, [saver('leader-home', log, 'fail'), saver('follower-home', log)]),
    /leader-home \(stall\)/,
  );
  assert.deepEqual(log, ['leader-home:2', 'follower-home:2']);
});

test('a hung move times out', async () => {
  await assert.rejects(rb.home(undefined, [saver('leader-home', [], 'hang')], 20), /leader-home \(.*timed out/);
});

test('status and saver detection', () => {
  assert.deepEqual(rb.asTeleopStatus({ running: true, last_error: '', cycles: 3 }), { running: true, last_error: '' });
  assert.equal(rb.asTeleopStatus({ ok: true }), undefined);
  assert.equal(rb.asTeleopStatus(null), undefined);
  assert.equal(rb.teleopState({ running: true, last_error: '' }), 'on');
  assert.equal(rb.teleopState({ running: false, last_error: '' }), 'off');
  assert.equal(rb.teleopState({ running: false, last_error: 'servo 3 timeout' }), 'error');
  assert.equal(rb.teleopState(undefined), 'off');
  assert.equal(rb.isSaver(['idle', 'update config', 'go to']), true);
  assert.equal(rb.isSaver(['off', 'on']), false);
});
