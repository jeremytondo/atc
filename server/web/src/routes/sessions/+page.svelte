<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listSessions,
    deleteSession,
    sessionActionLabel,
    messageFromError,
    type SessionListItem
  } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';
  import SessionRowActions from '$lib/sessions/session-row-actions.svelte';
  import { replaceRenamedSession } from '$lib/sessions/session-rename';

  let sessions = $state<SessionListItem[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busyId = $state('');

  const ORDER: SessionListItem['status'][] = ['live', 'ended'];
  const LABELS: Record<SessionListItem['status'], string> = {
    live: 'Live',
    ended: 'Ended'
  };

  function dotColor(status: string) {
    return (
      (
        {
          live: 'var(--dc-green)',
          ended: 'var(--dc-dim)'
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
      sessions = await listSessions();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  async function remove(s: SessionListItem) {
    // One deletion at a time: overlapping requests would fight over busyId
    // and could reload the list out of order.
    if (busyId) return;
    const label = s.name?.trim() || s.id;
    const effect =
      s.status === 'live'
        ? 'The running process will end and the session record will be removed.'
        : 'The session record will be permanently removed.';
    const ok = confirm(
      `Delete session "${label}"?\n\n${effect} Files on disk are not touched.`
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
              {#if s.status === 'live'}
                <a class="btn xs" href={`/sessions/${encodeURIComponent(s.id)}`}>Open</a>
              {/if}
              <SessionRowActions
                session={s}
                disabled={busyId !== ''}
                onRenamed={(renamed) => (sessions = replaceRenamedSession(sessions, renamed))}
              />
              <button class="btn xs" onclick={() => remove(s)} disabled={busyId !== ''}
                >Delete</button
              >
            </div>
          </div>
        </div>
      {/each}
    </div>
  {/each}
</div>
