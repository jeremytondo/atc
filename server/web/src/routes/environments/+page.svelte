<script lang="ts">
  import { onMount } from 'svelte';
  import { listEnvironments, messageFromError, type Environment } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';

  let environments = $state<Environment[]>([]);
  let loading = $state(false);
  let error = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      environments = await listEnvironments();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  onMount(load);
</script>

<svelte:head>
  <title>atc · Environments</title>
</svelte:head>

<div class="pad" style="max-width:780px">
  <h1 class="h1">Environments</h1>
  <p class="lede" style="margin-bottom:22px">
    The launch wrapper that decides how and where an action runs.
  </p>

  <ErrorBanner message={error} />

  {#if loading && environments.length === 0}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading environments…</p>
  {/if}

  <div style="display:flex;flex-direction:column;gap:14px">
    {#each environments as e (e.name)}
      <div class="card" style="padding:16px 18px">
        <div style="display:flex;align-items:center;gap:10px;margin-bottom:6px">
          <span style="font-size:15px;font-weight:600">{e.label || e.name}</span>
          <span class="mono" style="font-size:12px;color:var(--dc-dim)">{e.name}</span>
          {#if e.default}<span class="badge line">default</span>{/if}
        </div>
        {#if e.description}
          <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:0">{e.description}</p>
        {/if}
      </div>
    {/each}
  </div>
</div>
