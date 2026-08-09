<script lang="ts">
  import "../app.css"
  import { page } from "$app/state"

  let { children } = $props()
</script>

<div class="shell">
  <header class="topbar">
    <a class="brand" href="/"><span class="mark"></span>atc</a>
    <nav class="topnav">
      <a href="/" class:active={page.url.pathname === "/"}>Overview</a>
      <a href="/docs" class:active={page.url.pathname.startsWith("/docs")}>API Reference</a>
    </nav>
  </header>
  <main class="content">
    {@render children()}
  </main>
</div>
