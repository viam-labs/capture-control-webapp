// Recording-session engine. Pure: no Svelte or SDK imports. Each transition returns the
// next session plus the DoCommands to send to the capture-control-sensor, in order.

export type Settings = { task: string; episodes: number; episodeS: number | null; resetS: number; fps: number };
export type Phase = 'recording' | 'reset' | 'done';
// startedAt/endsAt bound the current phase; endsAt is Infinity for an open-ended episode.
export type Session = { id: string; settings: Settings; phase: Phase; episode: number; startedAt: number; endsAt: number };
export type Command =
  | { start_capture: true; frequency_hz: number; tags: string[] }
  | { stop_capture: true };
export type Step = [Session, Command[]];

const STOP: Command = { stop_capture: true };

export function tags(settings: Settings, id: string): string[] {
  return [`task:${settings.task}`, `session:${id}`, `fps:${settings.fps}`];
}

// frequency_hz must always be sent: the sensor's default_capture_frequency_hz is 0 (= no capture).
function startCapture(s: Session): Command {
  return { start_capture: true, frequency_hz: s.settings.fps, tags: tags(s.settings, s.id) };
}

// null seconds = open-ended: the phase only ends via next() (space bar).
const until = (now: number, seconds: number | null): number => (seconds == null ? Infinity : now + seconds * 1000);

export function start(settings: Settings, id: string, now: number): Step {
  const s: Session = { id, settings, phase: 'recording', episode: 1, startedAt: now, endsAt: until(now, settings.episodeS) };
  return [s, [startCapture(s)]];
}

/** Ends the current phase now (space bar). The last episode skips reset and goes to done. */
export function next(s: Session, now: number): Step {
  if (s.phase === 'recording') {
    if (s.episode >= s.settings.episodes) return [{ ...s, phase: 'done' }, [STOP]];
    return [{ ...s, phase: 'reset', startedAt: now, endsAt: until(now, s.settings.resetS) }, [STOP]];
  }
  if (s.phase === 'reset') {
    const r: Session = {
      ...s,
      phase: 'recording',
      episode: s.episode + 1,
      startedAt: now,
      endsAt: until(now, s.settings.episodeS),
    };
    return [r, [startCapture(r)]];
  }
  return [s, []];
}

export function tick(s: Session, now: number): Step {
  return now >= s.endsAt ? next(s, now) : [s, []];
}

/** Stops the session. Capture is already stopped during reset, so only recording needs stop_capture. */
export function stop(s: Session): Step {
  return [{ ...s, phase: 'done' }, s.phase === 'recording' ? [STOP] : []];
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
