// Robot-side helpers for the devrel:so101:teleop service and erh:vmodutils:arm-position-saver
// switches. No SDK imports: callers pass SDK clients, which match these shapes.

export type DoCommander = { doCommand(cmd: { command: string }): Promise<unknown> };
export type Saver = { name: string; setPosition(position: number): Promise<void> };
export type TeleopStatus = { running: boolean; last_error: string };

export const CALL_TIMEOUT_MS = 3_000;
const MOVE_TIMEOUT_MS = 30_000;
const GO_TO = 2; // arm-position-saver: 0 idle, 1 save current pose, 2 go to saved pose

export function asTeleopStatus(r: unknown): TeleopStatus | undefined {
  if (typeof r !== 'object' || r === null || typeof (r as { running?: unknown }).running !== 'boolean') return undefined;
  const { running, last_error } = r as { running: boolean; last_error?: unknown };
  return { running, last_error: typeof last_error === 'string' ? last_error : '' };
}

/** error = the loop stopped itself (max_consecutive_errors) and recorded why. */
export function teleopState(st: TeleopStatus | undefined): 'on' | 'off' | 'error' {
  if (st?.running) return 'on';
  return st?.last_error ? 'error' : 'off';
}

const SAVER_LABELS = ['idle', 'update config', 'go to'];
export const isSaver = (labels: string[]): boolean => labels.join('\n') === SAVER_LABELS.join('\n');

/** Rejects if p hasn't settled within ms, so a dropped connection can't wedge the command driver. */
export async function withTimeout<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`${what} timed out after ${ms / 1000} s`)), ms);
  });
  try {
    return await Promise.race([p, timeout]);
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Stops teleop (restoring leader torque), then moves every saver to its pose in parallel.
 * Teleop stays off: restarting it would drop leader torque while nobody may be holding it,
 * and the follower would copy the fall at full streaming speed. The operator presses T.
 */
export async function home(teleop: DoCommander | undefined, savers: Saver[], moveTimeoutMs = MOVE_TIMEOUT_MS): Promise<void> {
  if (teleop) {
    await withTimeout(teleop.doCommand({ command: 'stop' }), CALL_TIMEOUT_MS, 'Teleop stop').catch((e) => {
      throw new Error(`Teleop stop failed: ${e instanceof Error ? e.message : e}`);
    });
  }
  // allSettled: every arm finishes (or fails) before we report, so nothing is still moving when busy clears.
  const results = await Promise.allSettled(savers.map(async (s) => withTimeout(s.setPosition(GO_TO), moveTimeoutMs, 'move')));
  const failed = savers.flatMap((s, i) => {
    const r = results[i];
    return r.status === 'rejected' ? [`${s.name} (${r.reason instanceof Error ? r.reason.message : r.reason})`] : [];
  });
  if (failed.length) throw new Error(`Home failed: ${failed.join(', ')}. Arms may still be moving${teleop ? '; teleop is off' : ''}.`);
}
