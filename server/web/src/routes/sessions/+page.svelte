<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listSessions,
    terminateSession,
    archiveSession,
    unarchiveSession,
    deleteSession,
    sessionActionLabel,
    messageFromError,
    type SessionListItem
  } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';

  let sessions = $state<SessionListItem[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busyId = $state('');
  let includeArchived = $state(false);

  const ORDER = ['running', 'starting', 'failed', 'terminated'];
  const LABELS: Record<string, string> = {
    running: 'Running',
    starting: 'Starting',
    failed: 'Failed',
    terminated: 'Terminated'
  };

  function dotColor(status: string) {
    return (
      (
        {
          running: 'var(--dc-green)',
          starting: 'var(--dc-amber)',
          terminated: 'var(--dc-dim)',
          failed: 'var(--dc-red)'
        } as Record<string, string>
      )[status] ?? 'var(--dc-dim)'
    );
  }

  function timeAgo(value?: string) {
    if (!value) return '';
    const then = new Date(value).getTime();
    if (Number.isNaN(then)) return '';
    const secs = Math.max(0, Math.round((Date.now() - then) / 1000));
    if (secs < 60) return `${secs}s ago`;
    const mins = Math.round(secs / 60);
    if (mins < 60) return `${mins}m ago`;
    const hrs = Math.round(mins / 60);
    if (hrs < 24) return `${hrs}h ago`;
    return `${Math.round(hrs / 24)}d ago`;
  }

  let groups = $derived.by(() => {
    const present = [
      ...ORDER.filter((st) => sessions.some((s) => s.status === st)),
      ...[...new Set(sessions.map((s) => s.status))].filter((st) => !ORDER.includes(st))
    ];
    return present.map((st) => ({
      status: st,
      label: LABELS[st] ?? st,
      count: sessions.filter((s) => s.status === st).length,
      items: sessions.filter((s) => s.status === st)
    }));
  });

  async function load() {
    loading = true;
    error = '';
    try {
      sessions = await listSessions({ includeArchived });
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  function toggleArchived() {
    includeArchived = !includeArchived;
    void load();
  }

  async function stop(id: string) {
    busyId = id;
    error = '';
    try {
      await terminateSession(id);
      await load();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyId = '';
    }
  }

  async function archive(id: string) {
    busyId = id;
    error = '';
    try {
      await archiveSession(id);
      await load();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyId = '';
    }
  }

  async function unarchive(id: string) {
    busyId = id;
    error = '';
    try {
      await unarchiveSession(id);
      await load();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyId = '';
    }
  }

  async function remove(s: SessionListItem) {
    const label = s.name?.trim() || s.id;
    const ok = confirm(
      `Delete session "${label}"?\n\n` +
        `The session is stopped if it is still running and its record is removed. ` +
        `Files on disk are not touched.`
    );
    if (!ok) return;
    busyId = s.id;
    error = '';
    try {
      await deleteSession(s.id);
      await load();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyId = '';
    }
  }

  onMount(load);
</script>

<svelte:head>
  <title>atc · Sessions</title>
</svelte:head>

<div class="pad">
  <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
    <h1 class="h1">Sessions</h1>
    <div style="display:flex;gap:8px">
      <button class="btn" onclick={load} disabled={loading}>Refresh</button>
      <a class="btn primary" href="/reference/start">+ Start session</a>
    </div>
  </div>
  <p class="lede" style="margin-bottom:16px">
    Persistent terminals on this machine — including ones you start from the docs console.
  </p>

  <label
    style="display:flex;align-items:center;gap:7px;margin-bottom:16px;font-size:12.5px;color:var(--dc-mut);cursor:pointer"
  >
    <input type="checkbox" checked={includeArchived} onchange={toggleArchived} />
    Show archived
  </label>

  <ErrorBanner message={error} />

  {#if loading && sessions.length === 0}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading sessions…</p>
  {:else if sessions.length === 0}
    <div class="card" style="padding:26px;text-align:center;color:var(--dc-mut);font-size:13px">
      No sessions yet. <a href="/reference/start" style="color:var(--dc-acc)">Start one →</a>
    </div>
  {/if}

  {#each groups as g (g.status)}
    <div class="grouphead">
      <span class="tri"></span>
      <span
        style={`width:9px;height:9px;border-radius:50%;display:inline-block;background:${dotColor(g.status)}`}
      ></span>
      {g.label}<span class="gcount">{g.count}</span>
    </div>
    <div style="margin:2px 0 16px">
      {#each g.items as s (s.id)}
        <div class="irow">
          <span class="sdot" style={`background:${dotColor(s.status)}`}></span>
          <div class="sident">
            {#if s.name?.trim()}
              <span class="sname">{s.name}</span>
              <span class="ssub">{s.id}</span>
            {:else}
              <span class="sname asid">{s.id}</span>
            {/if}
          </div>
          <div class="imeta">
            {#if s.project}
              <a
                class="badge"
                href={`/projects/${encodeURIComponent(s.project.id)}`}
                style="color:var(--dc-acc);text-decoration:none">{s.project.name}</a
              >
            {/if}
            {#if s.workspace}
              <a
                class="badge"
                href={`/workspaces/${encodeURIComponent(s.workspace.id)}`}
                style="color:var(--dc-acc);text-decoration:none">{s.workspace.name}</a
              >
            {/if}
            <span class="badge">{sessionActionLabel(s)}</span>
            <span class="badge" style="color:var(--dc-dim)">{s.environment}</span>
            <span class="stime">{timeAgo(s.createdAt)}</span>
            <div class="iacts">
              {#if s.attachable}
                <a class="btn xs" href={`/sessions/${encodeURIComponent(s.id)}`}>Open</a>
              {/if}
              {#if s.status === 'running'}
                <button class="btn xs" onclick={() => stop(s.id)} disabled={busyId === s.id}
                  >Stop</button
                >
              {/if}
              {#if (s.status === 'failed' || s.status === 'terminated') && !s.archivedAt}
                <button class="btn xs" onclick={() => archive(s.id)} disabled={busyId === s.id}
                  >Archive</button
                >
              {/if}
              {#if s.archivedAt}
                <button class="btn xs" onclick={() => unarchive(s.id)} disabled={busyId === s.id}
                  >Unarchive</button
                >
              {/if}
              <button class="btn xs" onclick={() => remove(s)} disabled={busyId === s.id}
                >Delete</button
              >
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/each}
</div>
