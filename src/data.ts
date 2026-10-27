// Thin Data API layer for episode review. The browser uses the machine's own cookie credentials,
// which can read and tag this machine's sequences (verified on pi-arm6; see the spec).
import { createViamClient, type Credentials, type RobotClient, type Sequence, type ViamClient } from '@viamrobotics/sdk';
import { getHostAndCredentials } from './auth.js';
import { withReview, type Frame, type Review } from './review.ts';

export type Ids = { orgId: string; partId: string };

// Cache the promise so concurrent first calls share one transport; drop it on failure so Retry starts fresh.
let clientP: Promise<ViamClient> | undefined;
export function client(): Promise<ViamClient> {
  // auth.js is plain JS (type: string), so the cast is required for svelte-check.
  return (clientP ??= createViamClient({ credentials: getHostAndCredentials().credentials as Credentials }).catch((e) => {
    clientP = undefined;
    throw e;
  }));
}

let idsP: Promise<Ids> | undefined;
export function ids(robot: RobotClient): Promise<Ids> {
  return (idsP ??= robot
    .getCloudMetadata()
    .then((m) => ({ orgId: m.primaryOrgId, partId: m.machinePartId }))
    .catch((e) => {
      idsP = undefined;
      throw e;
    }));
}

export const ms = (t?: { toDate(): Date }): number => t?.toDate().getTime() ?? 0;

/** This machine's sequences (optionally one session's), oldest first. */
export async function listEpisodes({ orgId, partId, session }: Ids & { session?: string }): Promise<Sequence[]> {
  const data = (await client()).dataClient;
  const out: Sequence[] = [];
  let page: string | undefined;
  // ponytail: listSequences has no part filter, so this scans the whole org on every call.
  // Add a start-time cutoff or cache seen pages if the org grows.
  do {
    const r = await data.listSequences(orgId, page, 100);
    out.push(...r.sequences.filter((s) => s.partId === partId && (!session || s.sequenceTags.includes(`session:${session}`))));
    page = r.nextPageToken || undefined;
  } while (page);
  return out.sort((a, b) => ms(a.startTime) - ms(b.startTime));
}

/** Image frame metadata for a sequence (no bytes). */
export async function frameIndex(seqId: string): Promise<Frame[]> {
  const data = (await client()).dataClient;
  const out: Frame[] = [];
  let page: string | undefined;
  do {
    const r = await data.getSequenceBinaryData(seqId, page, 100);
    for (const d of r.data) {
      const m = d.metadata;
      const cm = m?.captureMetadata;
      if (!m || !cm?.mimeType.startsWith('image/')) continue;
      out.push({ id: m.binaryDataId, camera: cm.componentName, mime: cm.mimeType, time: ms(m.timeRequested ?? m.timeReceived) });
    }
    page = r.nextPageToken || undefined;
  } while (page);
  return out;
}

/** Bytes for up to ~20 frames, keyed by binary data id (the result order isn't guaranteed). */
export async function frameBytes(frames: Frame[]): Promise<Map<string, Blob>> {
  const data = (await client()).dataClient;
  const mime = new Map(frames.map((fr) => [fr.id, fr.mime]));
  const out = new Map<string, Blob>();
  for (const d of await data.binaryDataByIds(frames.map((fr) => fr.id), true)) {
    const id = d.metadata?.binaryDataId;
    if (id) out.set(id, new Blob([new Uint8Array(d.binary)], { type: mime.get(id) ?? 'image/jpeg' }));
  }
  return out;
}

/** Swaps the sequence's review tag. Re-reads first so tags changed elsewhere survive; returns the tags written. */
export async function setReview(seqId: string, state: Review): Promise<string[]> {
  const data = (await client()).dataClient;
  const tags = withReview((await data.getSequence(seqId)).sequenceTags, state);
  await data.updateSequence(seqId, undefined, tags, undefined, undefined, { paths: ['sequence_tags'] });
  return tags;
}
