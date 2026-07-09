<script lang="ts">
  import '../app.css';
  import { page } from '$app/state';
  import Shell from '$lib/shell.svelte';

  let { children } = $props();

  // The live terminal (/sessions/[id]) is a full-screen, immersive view, so it
  // renders outside the console shell. Every other route gets the sidebar shell.
  const bareRoutes = new Set(['/sessions/[id]']);
  let bare = $derived(bareRoutes.has(page.route.id ?? ''));
</script>

{#if bare}
  {@render children()}
{:else}
  <Shell>
    {@render children()}
  </Shell>
{/if}
