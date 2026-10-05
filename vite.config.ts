import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// base './' — Viam Apps and the local module server need relative asset paths.
export default defineConfig({ base: './', plugins: [svelte()] });
