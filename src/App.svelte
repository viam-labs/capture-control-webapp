<script lang="ts">
  import type { DialConf } from '@viamrobotics/sdk';
  import { ViamProvider } from '@viamrobotics/svelte-sdk';
  import { getHostAndCredentials } from './auth.js';
  import Recorder from './Recorder.svelte';

  const { host, credentials } = getHostAndCredentials();
  const dialConfigs: Record<string, DialConf> = {
    main: { host, credentials, signalingAddress: 'https://app.viam.com:443' } as DialConf,
  };
</script>

{#if host}
  <ViamProvider {dialConfigs}>
    <Recorder partID="main" />
  </ViamProvider>
{:else}
  <p>No machine credentials found. Open this app from Viam, or run it locally with <code>viam module local-app-testing</code>.</p>
{/if}

<style>
  :global(:root) {
    --bg: #fafafa; --fg: #1a1a1a; --muted: #666; --panel: #fff; --border: #ddd;
    --rec: #d22; --reset: #c80; --done: #2a7;
    color-scheme: light dark;
  }
  @media (prefers-color-scheme: dark) {
    :global(:root) { --bg: #111; --fg: #eee; --muted: #999; --panel: #1b1b1b; --border: #333; }
  }
  :global(body) { margin: 0; background: var(--bg); color: var(--fg); font: 15px/1.4 system-ui, sans-serif; }
</style>
