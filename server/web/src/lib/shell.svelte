<script lang="ts">
  import { onMount, type Snippet } from 'svelte';
  import { page } from '$app/state';
  import { ENDPOINTS, endpointByKey } from '$lib/docs/endpoints';
  import { getToken, setToken } from '$lib/api';

  let { children }: { children: Snippet } = $props();

  let apiOpen = $state(true);
  let token = $state('');

  let pathname = $derived(page.url.pathname);

  let crumb = $derived.by(() => {
    const p = pathname;
    if (p === '/') return 'Getting started';
    if (p.startsWith('/reference/')) {
      const key = p.slice('/reference/'.length);
      return 'Reference · ' + (endpointByKey(key)?.title ?? key);
    }
    if (p.startsWith('/sessions')) return 'Sessions';
    if (p.startsWith('/projects')) return 'Projects';
    if (p.startsWith('/actions')) return 'Actions';
    return 'atc';
  });

  function onToken(event: Event) {
    const value = (event.currentTarget as HTMLInputElement).value;
    token = value;
    setToken(value);
  }

  onMount(() => {
    token = getToken();
  });
</script>

<div class="app">
  <!-- ============ SIDEBAR ============ -->
  <div class="side">
    <div class="ws">
      <span class="wslogo"></span>
      <span class="wsname">atc</span>
      <span class="wschev">▾</span>
      <a class="wsadd" title="New session" href="/reference/start">+</a>
    </div>

    <a class="navi" class:on={pathname.startsWith('/sessions')} href="/sessions">
      <span class="ic"
        ><span style="width:11px;height:9px;border:1.4px solid currentColor;border-radius:2.5px"></span
        ></span
      >Sessions
    </a>
    <a class="navi" class:on={pathname.startsWith('/projects')} href="/projects">
      <span class="ic"
        ><span
          style="width:11px;height:9px;border:1.4px solid currentColor;border-radius:2.5px;border-top-width:3px"
        ></span></span
      >Projects
    </a>
    <a class="navi" class:on={pathname.startsWith('/actions')} href="/actions">
      <span class="ic"
        ><span
          style="width:0;height:0;border-left:8px solid currentColor;border-top:5px solid transparent;border-bottom:5px solid transparent"
        ></span></span
      >Actions
    </a>
    <div class="seclbl">Documentation</div>
    <a class="epitem" class:on={pathname === '/'} href="/">
      <span class="ic"
        ><span style="width:9px;height:11px;border:1.4px solid currentColor;border-radius:2px"></span
        ></span
      >Getting started
    </a>
    <button class="grptog" onclick={() => (apiOpen = !apiOpen)}>
      <span class="gchev" style={apiOpen ? 'transform:rotate(90deg)' : 'transform:rotate(0deg)'}></span
      >Reference
    </button>
    {#if apiOpen}
      {#each ENDPOINTS as endpoint (endpoint.key)}
        <a
          class="epitem"
          class:on={pathname === `/reference/${endpoint.key}`}
          href={`/reference/${endpoint.key}`}
        >
          <span class="ic"
            ><span style="width:4px;height:4px;border-radius:50%;background:currentColor;opacity:.55"
            ></span></span
          >{endpoint.title}
        </a>
      {/each}
    {/if}

    <div class="sidefoot">
      <div class="tokfield">
        <span style="color:var(--dc-dim);font-size:11px">API</span>
        <input type="password" placeholder="token (optional)" value={token} oninput={onToken} />
      </div>
    </div>
  </div>

  <!-- ============ CONTENT ============ -->
  <div class="content">
    <div class="topbar">
      <div class="crumb"><span>atc</span><span class="crsep">/</span><b>{crumb}</b></div>
      <div class="topright">
        <button class="iconbtn" title="History" aria-label="History">↻</button>
        <button class="iconbtn" title="Notifications" aria-label="Notifications">◔</button>
      </div>
    </div>

    {@render children()}
  </div>
</div>
