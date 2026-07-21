<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    getWorkspace,
    getProject,
    renameWorkspace,
    deleteWorkspace,
    deleteSession,
    listWorkspaceSessions,
    listActions,
    listEnvironments,
    startSession,
    sessionActionLabel,
    messageFromError,
    type Workspace,
    type Project,
    type SessionListItem,
    type Action,
    type Environment
  } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';
  import WorkspaceEditor from '$lib/workspaces/workspace-editor.svelte';
  import SessionRowActions from '$lib/sessions/session-row-actions.svelte';
  import { replaceRenamedSession } from '$lib/sessions/session-rename';

  const workspaceId = $derived(page.params.id ?? '');

  // The value the action select uses for "no action": the Interactive Shell.
  const interactiveShell = '';

  let workspace = $state<Workspace | null>(null);
  let project = $state<Project | null>(null);
  let sessions = $state<SessionListItem[]>([]);
  let actions = $state<Action[]>([]);
  let environments = $state<Environment[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busy = $state(false);
  let busySessionId = $state('');

  let renameOpen = $state(false);
  let saving = $state(false);
  let saveError = $state('');

  // Start Session form state. Params reset whenever the action changes because
  // each action declares its own spec.
  let startAction = $state(interactiveShell);
  let startEnvironment = $state('');
  let startName = $state('');
  let startPrompt = $state('');
  let startParams = $state<Record<string, string>>({});
  let starting = $state(false);
  let startError = $state('');

  let enabledActions = $derived(actions.filter((a) => a.enabled !== false));
  let selectedAction = $derived(enabledActions.find((a) => a.name === startAction) ?? null);
  let selectedParams = $derived(
    Object.entries(selectedAction?.params ?? {}).sort(([a], [b]) => a.localeCompare(b))
  );

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

  async function loadSessions() {
    sessions = await listWorkspaceSessions(workspaceId);
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [got, acts, envs] = await Promise.all([
        getWorkspace(workspaceId),
        listActions(),
        listEnvironments()
      ]);
      workspace = got;
      actions = acts;
      environments = envs;
      if (!startEnvironment) {
        startEnvironment = envs.find((e) => e.default)?.name ?? envs[0]?.name ?? '';
      }
      const [parent] = await Promise.all([getProject(got.projectId), loadSessions()]);
      project = parent;
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  function onActionChange(event: Event) {
    startAction = (event.currentTarget as HTMLSelectElement).value;
    startParams = {};
    startError = '';
  }

  async function removeWorkspace() {
    if (!workspace) return;
    busy = true;
    error = '';
    try {
      // Count every Live and Ended Session so the confirmation is honest
      // about the scope of the delete.
      let sessionNote = 'Its session history is removed';
      try {
        const affected = await listWorkspaceSessions(workspace.id);
        sessionNote = `${affected.length} session${affected.length === 1 ? '' : 's'} will be ended if Live and removed`;
      } catch {
        // Deliberately non-fatal: the confirmation still states the effect.
      }
      const ok = confirm(
        `Delete workspace "${workspace.name}"?\n\n` +
          `${sessionNote}. Files on disk are not touched.`
      );
      if (!ok) return;
      await deleteWorkspace(workspace.id);
      await goto(`/projects/${encodeURIComponent(workspace.projectId)}`);
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busy = false;
    }
  }

  function closeRename() {
    renameOpen = false;
    saving = false;
    saveError = '';
  }

  async function saveRename(values: { name: string }) {
    if (!workspace) return;
    saving = true;
    saveError = '';
    try {
      workspace = await renameWorkspace(workspace.id, values.name);
      closeRename();
    } catch (e) {
      saveError = messageFromError(e);
    } finally {
      saving = false;
    }
  }

  async function start() {
    if (!workspace || starting) return;
    starting = true;
    startError = '';
    try {
      const params: Record<string, string> = {};
      for (const [key, value] of Object.entries(startParams)) {
        if (value !== '') params[key] = value;
      }
      await startSession({
        workspaceId: workspace.id,
        // The interactive shell is an omitted action; it takes no params or
        // prompt, so those are only sent alongside a real action.
        action: startAction || undefined,
        environment: startEnvironment || undefined,
        params: startAction && Object.keys(params).length > 0 ? params : undefined,
        name: startName.trim() || undefined,
        prompt: startAction ? startPrompt.trim() || undefined : undefined
      });
      startName = '';
      startPrompt = '';
      await loadSessions();
    } catch (e) {
      startError = messageFromError(e);
    } finally {
      starting = false;
    }
  }

  async function removeSession(s: SessionListItem) {
    // One deletion at a time: overlapping requests would fight over
    // busySessionId and could reload the list out of order.
    if (busySessionId) return;
    const label = s.name?.trim() || s.id;
    const effect =
      s.status === 'live'
        ? 'The running process will end and the session record will be removed.'
        : 'The session record will be permanently removed.';
    if (!confirm(`Delete session "${label}"?\n\n${effect} Files on disk are not touched.`)) return;
    busySessionId = s.id;
    error = '';
    try {
      await deleteSession(s.id);
      await loadSessions();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busySessionId = '';
    }
  }

  onMount(load);
</script>

<svelte:head>
  <title>atc · {workspace?.name ?? 'Workspace'}</title>
</svelte:head>

<div class="pad" style="max-width:780px">
  <ErrorBanner message={error} />

  {#if loading && !workspace}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading workspace…</p>
  {:else if workspace}
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
      <div style="display:flex;align-items:center;gap:10px">
        <h1 class="h1" style="margin:0">{workspace.name}</h1>
      </div>
      <div style="display:flex;gap:8px">
        <button
          class="btn"
          onclick={() => {
            saveError = '';
            renameOpen = true;
          }}
          disabled={busy}>Rename</button
        >
        <button class="btn" onclick={removeWorkspace} disabled={busy}>Delete</button>
      </div>
    </div>
    <p class="lede" style="margin-bottom:20px">
      <a href={`/projects/${encodeURIComponent(workspace.projectId)}`} style="color:var(--dc-acc)"
        >← {project?.name ?? workspace.projectId}</a
      >
    </p>

    <div class="card" style="margin-bottom:26px">
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">id</span>
        <span class="mono" style="font-size:12.5px">{workspace.id}</span>
      </div>
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">project</span>
        <span class="mono" style="font-size:12.5px">{workspace.projectId}</span>
      </div>
      {#if project}
        <div class="row">
          <span style="color:var(--dc-mut);font-size:12.5px">workingDir</span>
          <span class="mono" style="font-size:12.5px">{project.workingDir}</span>
        </div>
      {/if}
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">createdAt</span>
        <span class="mono" style="font-size:12.5px">{workspace.createdAt}</span>
      </div>
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">updatedAt</span>
        <span class="mono" style="font-size:12.5px">{workspace.updatedAt}</span>
      </div>
    </div>

    <div class="seclabel">Start session</div>
    <div class="card" style="padding:16px;margin-bottom:26px">
      <div class="fieldgrid" style="margin-bottom:12px">
        <div>
          <label class="lbl" for="start-action">Action</label>
          <select id="start-action" class="sel" value={startAction} onchange={onActionChange}>
            <option value={interactiveShell}>(interactive shell)</option>
            {#each enabledActions as a (a.name)}
              <option value={a.name}>{a.label || a.name}</option>
            {/each}
          </select>
        </div>
        <div>
          <label class="lbl" for="start-env">Environment</label>
          <select
            id="start-env"
            class="sel"
            value={startEnvironment}
            onchange={(e) => (startEnvironment = e.currentTarget.value)}
          >
            {#each environments as env (env.name)}
              <option value={env.name}>{env.label || env.name}</option>
            {/each}
          </select>
        </div>
      </div>
      <div style="margin-bottom:12px">
        <label class="lbl" for="start-name">Name</label>
        <input
          id="start-name"
          class="inp"
          value={startName}
          oninput={(e) => (startName = e.currentTarget.value)}
          placeholder="optional"
        />
      </div>
      {#if selectedAction?.prompt}
        <div style="margin-bottom:12px">
          <label class="lbl" for="start-prompt">Prompt</label>
          <textarea
            id="start-prompt"
            class="ta"
            value={startPrompt}
            oninput={(e) => (startPrompt = e.currentTarget.value)}
            placeholder="optional starting prompt…"
          ></textarea>
        </div>
      {/if}
      {#each selectedParams as [name, spec] (name)}
        <div style="margin-bottom:12px">
          <label class="lbl" for={`start-param-${name}`}>{spec.label || name}</label>
          {#if spec.type === 'bool'}
            <select
              id={`start-param-${name}`}
              class="sel"
              value={startParams[name] ?? ''}
              onchange={(e) => (startParams = { ...startParams, [name]: e.currentTarget.value })}
            >
              <option value="">(default)</option>
              <option value="true">true</option>
              <option value="false">false</option>
            </select>
          {:else}
            <select
              id={`start-param-${name}`}
              class="sel"
              value={startParams[name] ?? ''}
              onchange={(e) => (startParams = { ...startParams, [name]: e.currentTarget.value })}
            >
              <option value="">(default)</option>
              {#each spec.values ?? [] as v (v)}
                <option value={v}>{v}</option>
              {/each}
            </select>
          {/if}
        </div>
      {/each}

      <ErrorBanner message={startError} />
      <button class="btn primary" disabled={starting} onclick={start}>
        {starting ? 'Starting…' : 'Start session'}
      </button>
    </div>
    <div class="seclabel">Sessions</div>
    {#if sessions.length === 0}
      <div class="card" style="padding:20px;text-align:center;color:var(--dc-mut);font-size:13px">
        No sessions in this workspace yet.
      </div>
    {:else}
      <div>
        {#each sessions as s (s.id)}
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
              <span class="badge">{sessionActionLabel(s)}</span>
              <span class="badge" style="color:var(--dc-dim)">{s.status}</span>
              <span class="stime">{timeAgo(s.createdAt)}</span>
              <div class="iacts">
                {#if s.status === 'live'}
                  <a class="btn xs" href={`/sessions/${encodeURIComponent(s.id)}`}>Open</a>
                {/if}
                <SessionRowActions
                  session={s}
                  disabled={busySessionId !== ''}
                  onRenamed={(renamed) => (sessions = replaceRenamedSession(sessions, renamed))}
                />
                <button class="btn xs" onclick={() => removeSession(s)} disabled={busySessionId !== ''}
                  >Delete</button
                >
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

{#if renameOpen && workspace}
  <WorkspaceEditor
    mode="rename"
    source={workspace}
    {saving}
    {saveError}
    onSave={saveRename}
    onCancel={closeRename}
  />
{/if}
