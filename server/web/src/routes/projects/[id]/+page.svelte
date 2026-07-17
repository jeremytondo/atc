<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import {
    getProject,
    renameProject,
    archiveProject,
    unarchiveProject,
    deleteProject,
    listProjectSessions,
    listWorkspaces,
    createWorkspace,
    renameWorkspace,
    archiveWorkspace,
    unarchiveWorkspace,
    deleteWorkspace,
    deleteSession,
    listWorkspaceSessions,
    sessionActionLabel,
    messageFromError,
    type Project,
    type SessionListItem,
    type Workspace
  } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';
  import ProjectEditor from '$lib/projects/project-editor.svelte';
  import WorkspaceEditor from '$lib/workspaces/workspace-editor.svelte';

  const projectId = $derived(page.params.id ?? '');

  let project = $state<Project | null>(null);
  let workspaces = $state<Workspace[]>([]);
  let sessions = $state<SessionListItem[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busy = $state(false);
  let busyWorkspaceId = $state('');
  let includeArchivedWorkspaces = $state(false);
  let busySessionId = $state('');

  let renameOpen = $state(false);
  let saving = $state(false);
  let saveError = $state('');

  // Workspace editor state: mode plus the workspace being renamed (null for
  // create).
  let workspaceEditorOpen = $state(false);
  let workspaceEditorMode = $state<'create' | 'rename'>('create');
  let workspaceEditorSource = $state<Workspace | null>(null);
  let workspaceSaving = $state(false);
  let workspaceSaveError = $state('');

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
    sessions = await listProjectSessions(projectId);
  }

  async function loadWorkspaces() {
    workspaces = await listWorkspaces({
      projectId,
      includeArchived: includeArchivedWorkspaces
    });
  }

  async function load() {
    loading = true;
    error = '';
    try {
      project = await getProject(projectId);
      await Promise.all([loadWorkspaces(), loadSessions()]);
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  function toggleArchivedWorkspaces() {
    includeArchivedWorkspaces = !includeArchivedWorkspaces;
    void loadWorkspaces().catch((e) => (error = messageFromError(e)));
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

  async function removeProject() {
    if (!project) return;
    const ok = confirm(
      `Delete project "${project.name}"?\n\n` +
        `Only the project record is removed; a project with workspaces cannot be deleted. ` +
        `Files on disk are not touched.`
    );
    if (!ok) return;
    busy = true;
    error = '';
    try {
      await deleteProject(project.id);
      await goto('/projects');
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

  function openNewWorkspace() {
    workspaceEditorMode = 'create';
    workspaceEditorSource = null;
    workspaceSaveError = '';
    workspaceEditorOpen = true;
  }

  function openRenameWorkspace(ws: Workspace) {
    workspaceEditorMode = 'rename';
    workspaceEditorSource = ws;
    workspaceSaveError = '';
    workspaceEditorOpen = true;
  }

  function closeWorkspaceEditor() {
    workspaceEditorOpen = false;
    workspaceSaving = false;
    workspaceSaveError = '';
  }

  async function saveWorkspace(values: { name: string }) {
    workspaceSaving = true;
    workspaceSaveError = '';
    try {
      if (workspaceEditorMode === 'create') {
        await createWorkspace(projectId, values.name);
      } else if (workspaceEditorSource) {
        await renameWorkspace(workspaceEditorSource.id, values.name);
      }
      await loadWorkspaces();
      closeWorkspaceEditor();
    } catch (e) {
      workspaceSaveError = messageFromError(e);
    } finally {
      workspaceSaving = false;
    }
  }

  async function workspaceArchiveToggle(ws: Workspace) {
    busyWorkspaceId = ws.id;
    error = '';
    try {
      if (ws.archivedAt) {
        await unarchiveWorkspace(ws.id);
      } else {
        await archiveWorkspace(ws.id);
      }
      await loadWorkspaces();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyWorkspaceId = '';
    }
  }

  async function removeWorkspace(ws: Workspace) {
    busyWorkspaceId = ws.id;
    error = '';
    try {
      // Count the sessions the delete will remove so the confirmation is
      // honest about scope; a lookup failure falls back to an uncounted
      // message rather than blocking the delete.
      let sessionNote = 'Its session history is removed';
      try {
        const affected = await listWorkspaceSessions(ws.id);
        sessionNote = `${affected.length} session${affected.length === 1 ? '' : 's'} will be ended if Live and removed`;
      } catch {
        // Deliberately non-fatal: the confirmation still states the effect.
      }
      const ok = confirm(
        `Delete workspace "${ws.name}"?\n\n` +
          `${sessionNote}. Files on disk are not touched.`
      );
      if (!ok) return;
      await deleteWorkspace(ws.id);
      await Promise.all([loadWorkspaces(), loadSessions()]);
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyWorkspaceId = '';
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
        <button class="btn" onclick={removeProject} disabled={busy}>Delete</button>
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

    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:8px">
      <div class="seclabel" style="margin:0">Workspaces</div>
      <div style="display:flex;align-items:center;gap:14px">
        <label
          style="display:flex;align-items:center;gap:7px;font-size:12.5px;color:var(--dc-mut);cursor:pointer"
        >
          <input
            type="checkbox"
            checked={includeArchivedWorkspaces}
            onchange={toggleArchivedWorkspaces}
          />
          Show archived
        </label>
        {#if !project.archivedAt}
          <button class="btn xs" onclick={openNewWorkspace}>+ New workspace</button>
        {/if}
      </div>
    </div>
    {#if workspaces.length === 0}
      <div
        class="card"
        style="padding:20px;text-align:center;color:var(--dc-mut);font-size:13px;margin-bottom:26px"
      >
        No workspaces yet. Create one to start sessions in this project.
      </div>
    {:else}
      <div style="margin-bottom:26px">
        {#each workspaces as ws (ws.id)}
          <div class="irow" style={ws.archivedAt ? 'opacity:.55' : ''}>
            <div class="sident">
              <span class="sname">{ws.name}</span>
              <span class="ssub">{ws.id}</span>
            </div>
            <div class="imeta">
              {#if ws.archivedAt}<span class="badge line">archived</span>{/if}
              <span class="stime">{timeAgo(ws.createdAt)}</span>
              <div class="iacts">
                <a class="btn xs" href={`/workspaces/${encodeURIComponent(ws.id)}`}>Open</a>
                <button
                  class="btn xs"
                  onclick={() => openRenameWorkspace(ws)}
                  disabled={busyWorkspaceId === ws.id}>Rename</button
                >
                <button
                  class="btn xs"
                  onclick={() => workspaceArchiveToggle(ws)}
                  disabled={busyWorkspaceId === ws.id}
                >
                  {ws.archivedAt ? 'Unarchive' : 'Archive'}
                </button>
                <button
                  class="btn xs"
                  onclick={() => removeWorkspace(ws)}
                  disabled={busyWorkspaceId === ws.id}>Delete</button
                >
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}

    <div class="seclabel">Sessions</div>
    {#if sessions.length === 0}
      <div class="card" style="padding:20px;text-align:center;color:var(--dc-mut);font-size:13px">
        No sessions in this project yet. Open a workspace to start one.
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
              {#if s.workspace}
                <a
                  class="badge"
                  href={`/workspaces/${encodeURIComponent(s.workspace.id)}`}
                  style="color:var(--dc-acc);text-decoration:none">{s.workspace.name}</a
                >
              {/if}
              <span class="badge">{sessionActionLabel(s)}</span>
              <span class="badge" style="color:var(--dc-dim)">{s.status}</span>
              <span class="stime">{timeAgo(s.createdAt)}</span>
              <div class="iacts">
                {#if s.status === 'live'}
                  <a class="btn xs" href={`/sessions/${encodeURIComponent(s.id)}`}>Open</a>
                {/if}
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

{#if workspaceEditorOpen}
  <WorkspaceEditor
    mode={workspaceEditorMode}
    source={workspaceEditorSource}
    saving={workspaceSaving}
    saveError={workspaceSaveError}
    onSave={saveWorkspace}
    onCancel={closeWorkspaceEditor}
  />
{/if}
