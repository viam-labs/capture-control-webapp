<script module lang="ts">
  import { StreamClient, type RobotClient } from '@viamrobotics/sdk';

  // One StreamClient per RobotClient for the app's lifetime. StreamClient re-adds its
  // streams on every CONNECTED event, and a single instance avoids the orphaned-client
  // race that leaves the svelte-sdk's <CameraStream> black after a reconnect.
  const streamClients = new WeakMap<RobotClient, StreamClient>();
  function streamClientFor(client: RobotClient): StreamClient {
    let sc = streamClients.get(client);
    if (!sc) {
      sc = new StreamClient(client);
      streamClients.set(client, sc);
    }
    return sc;
  }
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { MachineConnectionEvent } from '@viamrobotics/sdk';
  import { useConnectionStatus, useRobotClient } from '@viamrobotics/svelte-sdk';

  let { partID, name }: { partID: string; name: string } = $props();

  const robot = useRobotClient(() => partID);
  const status = useConnectionStatus(() => partID);
  let video = $state<HTMLVideoElement>();
  let live = $state(false);

  $effect(() => {
    const client = robot.current;
    const el = video;
    if (!client || !el) return;
    const streams = streamClientFor(client);
    // Stream ids use '+' in place of ':' for remote resources.
    const ids = [name, name.replaceAll(':', '+')];
    let lastRequest = 0;

    // Listen for the whole lifetime of the component, not once like getStream(): every
    // renegotiation (including after a reconnect) delivers a new track.
    const onTrack = (e: RTCTrackEvent) => {
      const [stream] = e.streams;
      if (stream && ids.includes(stream.id)) el.srcObject = stream;
    };
    streams.on('track', onTrack);

    // Watchdog: while connected without a live video track, re-request the stream.
    // remove() first so the server renegotiates a fresh track even if it thinks the
    // stream is already added.
    const check = () => {
      const stream = el.srcObject as MediaStream | null;
      live = !!stream?.getVideoTracks().some((t) => t.readyState === 'live' && !t.muted);
      if (live || status.current !== MachineConnectionEvent.CONNECTED) return;
      if (Date.now() - lastRequest < 5000) return;
      lastRequest = Date.now();
      streams
        .remove(name)
        .catch(() => {})
        .then(() => streams.add(name))
        .catch(() => {});
    };
    untrack(check);
    const timer = setInterval(check, 1000);

    return () => {
      clearInterval(timer);
      streams.off('track', onTrack);
      streams.remove(name).catch(() => {});
      el.srcObject = null;
    };
  });
</script>

<figure>
  <video bind:this={video} muted autoplay playsinline></video>
  <figcaption>{name}{#if !live} · connecting…{/if}</figcaption>
</figure>

<style>
  figure { position: relative; margin: 0; min-height: 0; display: grid; }
  video { width: 100%; height: 100%; min-height: 0; object-fit: contain; background: #000; }
  figcaption {
    position: absolute; left: 8px; bottom: 8px; padding: 2px 6px; border-radius: 4px;
    background: rgba(0, 0, 0, 0.55); color: #fff; font-size: 13px;
  }
</style>
