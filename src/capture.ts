// Recording-session engine. Pure: no Svelte or SDK imports. Each transition returns the
// next session plus the DoCommands to send to the capture-control-sensor, in order.

export type Settings = { episodes: number; episodeS: number | null; resetS: number; fps: number };
export type Phase = 'recording' | 'reset' | 'done';
// startedAt/endsAt bound the current phase; endsAt is Infinity for an open-ended phase.
// episodeTask tags the open (or last) episode; counts holds ended episodes per task.
export type Session = {
  id: string;
  settings: Settings;
  phase: Phase;
  episode: number;
  startedAt: number;
  endsAt: number;
  episodeTask: string;
  counts: Record<string, number>;
};
export type Command =
  | { start_capture: true; frequency_hz: number; tags: string[] }
  | { stop_capture: true };
export type Step = [Session, Command[]];

const STOP: Command = { stop_capture: true };

// frequency_hz must always be sent: the sensor's default_capture_frequency_hz is 0 (= no capture).
function startCapture(s: Session): Command {
  return {
    start_capture: true,
    frequency_hz: s.settings.fps,
    tags: [`task:${s.episodeTask}`, `session:${s.id}`, `fps:${s.settings.fps}`],
  };
}

// Every ended recording wrote a sequence, so it counts toward its task.
const counted = (s: Session): Record<string, number> => ({ ...s.counts, [s.episodeTask]: (s.counts[s.episodeTask] ?? 0) + 1 });

// null seconds = open-ended: the phase only ends via next() (space bar).
const until = (now: number, seconds: number | null): number => (seconds == null ? Infinity : now + seconds * 1000);

export function start(settings: Settings, task: string, id: string, now: number): Step {
  const s: Session = {
    id, settings, phase: 'recording', episode: 1, startedAt: now, endsAt: until(now, settings.episodeS),
    episodeTask: task, counts: {},
  };
  return [s, [startCapture(s)]];
}

/**
 * Ends the current phase now (space bar). The last episode skips reset and goes to done.
 * task tags the episode this starts, if any; the open episode keeps its own tag.
 */
export function next(s: Session, now: number, task: string = s.episodeTask): Step {
  if (s.phase === 'recording') {
    const counts = counted(s);
    if (s.episode >= s.settings.episodes) return [{ ...s, phase: 'done', counts }, [STOP]];
    return [{ ...s, phase: 'reset', counts, startedAt: now, endsAt: until(now, s.settings.resetS) }, [STOP]];
  }
  if (s.phase === 'reset') {
    const r: Session = {
      ...s,
      phase: 'recording',
      episode: s.episode + 1,
      episodeTask: task,
      startedAt: now,
      endsAt: until(now, s.settings.episodeS),
    };
    return [r, [startCapture(r)]];
  }
  return [s, []];
}

export function tick(s: Session, now: number, task?: string): Step {
  return now >= s.endsAt ? next(s, now, task) : [s, []];
}

/** Holds a reset open until next() (space): used while teleop is off, so no episode auto-starts. */
export function hold(s: Session): Session {
  return s.phase === 'reset' ? { ...s, endsAt: Infinity } : s;
}

/** Stops the session. Capture is already stopped during reset, so only recording needs stop_capture. */
export function stop(s: Session): Step {
  if (s.phase === 'recording') return [{ ...s, phase: 'done', counts: counted(s) }, [STOP]];
  return [{ ...s, phase: 'done' }, []];
}

export function remainingMs(s: Session, now: number): number {
  return s.phase === 'done' ? 0 : Math.max(0, s.endsAt - now);
}

export function elapsedMs(s: Session, now: number): number {
  return Math.max(0, now - s.startedAt);
}

// Helpers for reading viam:capture-control:capture-control-sensor Readings().
type Readings = Record<string, unknown>;

export const isController = (r: Readings): boolean => Array.isArray(r.overrides) && Array.isArray(r.sequences);

export const controlledResources = (r: Readings): string[] =>
  (r.overrides as { resource_name: string }[]).map((o) => o.resource_name);

export const hasOpenSequence = (r: Readings): boolean => (r.sequences as unknown[]).length > 0;
