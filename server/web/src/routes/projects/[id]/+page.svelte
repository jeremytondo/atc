<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import {
    getProject,
    renameProject,
    archiveProject,
    unarchiveProject,
    listProjectSessions,
    listActions,
    listEnvironments,
    startSession,
    messageFromError,
    type Project,
    type SessionListItem,
    type Action,
    type Environment
  } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';
  import ProjectEditor from '$lib/projects/project-editor.svelte';

  const projectId = $derived(page.params.id ?? '');

  let project = $state<Project | null>(null);
  let sessions = $state<SessionListItem[]>([]);
  let actions = $state<Action[]>([]);
  let environments = $state<Environment[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busy = $state(false);
  let includeArchived = $state(false);

  let renameOpen = $state(false);
  let saving = $state(false);
  let saveError = $state('');

  // Start Session form state. Params reset whenever the action changes because
  // each action declares its own spec.
  let startAction = $state('');
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

  async function loadSessions() {
    sessions = await listProjectSessions(projectId, { includeArchived });
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [got, acts, envs] = await Promise.all([
        getProject(projectId),
        listActions(),
        listEnvironments()
      ]);
      project = got;
      actions = acts;
      environments = envs;
      if (!startAction) {
        startAction = acts.find((a) => a.enabled !== false)?.name ?? '';
      }
      if (!startEnvironment) {
        startEnvironment = envs.find((e) => e.default)?.name ?? envs[0]?.name ?? '';
      }
      await loadSessions();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  function toggleArchivedSessions() {
    includeArchived = !includeArchived;
    void loadSessions().catch((e) => (error = messageFromError(e)));
  }

  function onActionChange(event: Event) {
    startAction = (event.currentTarget as HTMLSelectElement).value;
    startParams = {};
    startError = '';
  }

  async function archiveToggle() {
    if (!project) return;
    busy = true;
    error = '';
    try {
      project = project.archivedAt
        ? await unarchiveProject(project.id)
        : await archiveProject(project.id);
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
    if (!project) return;
    saving = true;
    saveError = '';
    try {
      project = await renameProject(project.id, values.name);
      closeRename();
    } catch (e) {
      saveError = messageFromError(e);
    } finally {
      saving = false;
    }
  }

  async function start() {
    if (!project || !startAction || starting) return;
    starting = true;
    startError = '';
    try {
      const params: Record<string, string> = {};
      for (const [key, value] of Object.entries(startParams)) {
        if (value !== '') params[key] = value;
      }
      await startSession({
        action: startAction,
        environment: startEnvironment || undefined,
        params: Object.keys(params).length > 0 ? params : undefined,
        name: startName.trim() || undefined,
        prompt: startPrompt.trim() || undefined,
        projectId: project.id
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

  onMount(load);
</script>

<svelte:head>
  <title>atc · {project?.name ?? 'Project'}</title>
</svelte:head>

<div class="pad" style="max-width:780px">
  <ErrorBanner message={error} />

  {#if loading && !project}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading project…</p>
  {:else if project}
    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
      <div style="display:flex;align-items:center;gap:10px">
        <h1 class="h1" style="margin:0">{project.name}</h1>
        {#if project.archivedAt}<span class="badge line">archived</span>{/if}
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
        <button class="btn" onclick={archiveToggle} disabled={busy}>
          {project.archivedAt ? 'Unarchive' : 'Archive'}
        </button>
      </div>
    </div>
    <p class="lede" style="margin-bottom:20px">
      <a href="/projects" style="color:var(--dc-acc)">← All projects</a>
    </p>

    <div class="card" style="margin-bottom:26px">
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">id</span>
        <span class="mono" style="font-size:12.5px">{project.id}</span>
      </div>
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">workingDir</span>
        <span class="mono" style="font-size:12.5px">{project.workingDir}</span>
      </div>
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">createdAt</span>
        <span class="mono" style="font-size:12.5px">{project.createdAt}</span>
      </div>
      <div class="row">
        <span style="color:var(--dc-mut);font-size:12.5px">updatedAt</span>
        <span class="mono" style="font-size:12.5px">{project.updatedAt}</span>
      </div>
      {#if project.archivedAt}
        <div class="row">
          <span style="color:var(--dc-mut);font-size:12.5px">archivedAt</span>
          <span class="mono" style="font-size:12.5px">{project.archivedAt}</span>
        </div>
      {/if}
    </div>

    {#if !project.archivedAt}
      <div class="seclabel">Start session</div>
      <div class="card" style="padding:16px;margin-bottom:26px">
        <div class="fieldgrid" style="margin-bottom:12px">
          <div>
            <label class="lbl" for="start-action">Action</label>
            <select id="start-action" class="sel" value={startAction} onchange={onActionChange}>
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
        <button
          class="btn primary"
          class:off={!startAction}
          disabled={!startAction || starting}
          onclick={start}
        >
          {starting ? 'Starting…' : 'Start session'}
        </button>
      </div>
    {/if}

    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:8px">
      <div class="seclabel" style="margin:0">Sessions</div>
      <label
        style="display:flex;align-items:center;gap:7px;font-size:12.5px;color:var(--dc-mut);cursor:pointer"
      >
        <input type="checkbox" checked={includeArchived} onchange={toggleArchivedSessions} />
        Show archived
      </label>
    </div>
    {#if sessions.length === 0}
      <div class="card" style="padding:20px;text-align:center;color:var(--dc-mut);font-size:13px">
        No sessions in this project yet.
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
              <span class="badge">{s.action}</span>
              <span class="badge" style="color:var(--dc-dim)">{s.status}</span>
              <span class="stime">{timeAgo(s.createdAt)}</span>
              <div class="iacts">
                <a class="btn xs" href={`/sessions/${encodeURIComponent(s.id)}`}>Open</a>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

{#if renameOpen && project}
  <ProjectEditor
    mode="rename"
    source={project}
    {saving}
    {saveError}
    onSave={saveRename}
    onCancel={closeRename}
  />
{/if}
