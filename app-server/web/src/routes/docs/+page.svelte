<script lang="ts">
  import { onMount } from "svelte"
  import { createApiReference } from "@scalar/api-reference"

  let host: HTMLDivElement

  onMount(() => {
    const app = createApiReference(host, {
      // The document the server already exposes; same-origin in dev (via the
      // Vite proxy) and in production.
      url: "/openapi.json",
      hideClientButton: true,
      hideDarkModeToggle: true,
      forceDarkModeState: "dark",
      // Keep the page self-contained: no font fetches from CDNs.
      withDefaultFonts: false,
    })
    return () => app.destroy()
  })
</script>

<div class="docs-host" bind:this={host}></div>
