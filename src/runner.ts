// Episode-runner helpers. Pure: no Svelte or SDK imports.

export type RunnerState = 'no_session' | 'idle' | 'starting' | 'policy' | 'correction';
export type Outcome = 'success' | 'failure' | 'takeover' | 'error' | 'stopped';
export type RunnerStatus = {
  episode_runner: true;
  state: RunnerState;
  session: { id: string; task: string; checkpoint: string; fps: number; episodes: number; counts: Record<string, number> } | null;
  episode: { id: number; source: 'policy' | 'correction'; started: number } | null;
  last_error: string;
};
export type Segment = {
  session: string; episode: number; source: 'policy' | 'correction'; task: string; checkpoint: string;
  t_start: string; t_end: string; outcome: Outcome; error: string; captured: boolean;
};

export function asRunnerStatus(r: unknown): RunnerStatus | undefined {
  const o = r as Partial<RunnerStatus> | null;
  return o && o.episode_runner === true && typeof o.state === 'string' ? (o as RunnerStatus) : undefined;
}

/** The outcome for a synced sequence, matched by its session, episode and source tags; undefined if not in the log. */
export function outcomeFor(tags: string[], segs: Segment[]): Outcome | undefined {
  const get = (k: string) => tags.find((t) => t.startsWith(`${k}:`))?.slice(k.length + 1);
  const session = get('session'), episode = Number(get('episode')), source = get('source');
  return segs.find((s) => s.session === session && s.episode === episode && s.source === source)?.outcome;
}

export const withOutcome = (tags: string[], o: Outcome): string[] => [...tags.filter((t) => !t.startsWith('outcome:')), `outcome:${o}`];

export function summary(segs: Segment[]) {
  const policy = segs.filter((s) => s.source === 'policy');
  const n = (o: Outcome) => policy.filter((s) => s.outcome === o).length;
  const judged = n('success') + n('failure') + n('takeover');
  return {
    episodes: policy.length,
    success: n('success'), failure: n('failure'), takeover: n('takeover'), error: n('error'), stopped: n('stopped'),
    successRate: judged ? n('success') / judged : undefined,
    corrections: segs.filter((s) => s.source === 'correction').length,
  };
}

/** The runner needs a heartbeat in these states. */
export const needsHeartbeat = (state: string): boolean => state === 'starting' || state === 'policy' || state === 'correction';
/** Only these allow home and a fresh episode; an unknown state string counts as busy. */
export const isIdle = (state: string): boolean => state === 'idle' || state === 'no_session';

/** Segments that started a capture: only these can have a synced sequence to review. */
export const capturedCount = (segs: Segment[]): number => segs.filter((s) => s.captured).length;
