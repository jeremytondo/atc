<script lang="ts">
  // Shared error banner, extracted from the inline markup every page carried.
  // Renders nothing when the message is empty so callers can pass their error
  // state unconditionally.
  let { message = '' }: { message?: string } = $props();
</script>

{#if message}
  <div
    class="card"
    style="border-color:color-mix(in srgb,var(--dc-red) 40%,transparent);background:color-mix(in srgb,var(--dc-red) 10%,transparent);color:var(--dc-red);padding:10px 14px;margin-bottom:16px;font-size:12.5px"
  >
    {message}
  </div>
{/if}
