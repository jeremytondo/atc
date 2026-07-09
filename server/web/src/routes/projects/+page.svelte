<script lang="ts">
  import { onMount } from 'svelte';
  import { listProjects, createProject, messageFromError, type Project } from '$lib/api';
  import ErrorBanner from '$lib/error-banner.svelte';
  import ProjectEditor from '$lib/projects/project-editor.svelte';

  let projects = $state<Project[]>([]);
  let loading = $state(false);
  let error = $state('');
  let includeArchived = $state(false);

  let editorOpen = $state(false);
  let saving = $state(false);
  let saveError = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      projects = await listProjects({ includeArchived });
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

  function openNew() {
    saveError = '';
    editorOpen = true;
  }

  function closeEditor() {
    editorOpen = false;
    saving = false;
    saveError = '';
  }

  async function save(values: { name: string; workingDir: string }) {
    saving = true;
    saveError = '';
    try {
      await createProject(values.name, values.workingDir);
      await load();
      closeEditor();
    } catch (e) {
      saveError = messageFromError(e);
    } finally {
      saving = false;
    }
  }

  onMount(load);
</script>

<svelte:head>
  <title>Atelier Code · Projects</title>
</svelte:head>

<div class="pad" style="max-width:780px">
  <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
    <h1 class="h1">Projects</h1>
    <div style="display:flex;gap:8px">
      <button class="btn" onclick={load} disabled={loading}>Refresh</button>
      <button class="btn primary" onclick={openNew}>+ New project</button>
    </div>
  </div>
  <p class="lede" style="margin-bottom:16px">
    A project names one directory on this machine and groups the sessions you start in it.
  </p>

  <label style="display:flex;align-items:center;gap:7px;margin-bottom:16px;font-size:12.5px;color:var(--dc-mut);cursor:pointer">
    <input type="checkbox" checked={includeArchived} onchange={toggleArchived} />
    Show archived
  </label>

  <ErrorBanner message={error} />

  {#if loading && projects.length === 0}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading projects…</p>
  {:else if projects.length === 0}
    <div class="card" style="padding:26px;text-align:center;color:var(--dc-mut);font-size:13px">
      No projects yet. Create one to group sessions around a directory.
    </div>
  {/if}

  <div style="display:flex;flex-direction:column;gap:12px">
    {#each projects as p (p.id)}
      <a
        class="card"
        href={`/projects/${encodeURIComponent(p.id)}`}
        style="padding:15px 17px;display:block;text-decoration:none;color:inherit;{p.archivedAt
          ? 'opacity:.55'
          : ''}"
      >
        <div style="display:flex;align-items:center;gap:10px">
          <span style="font-size:14.5px;font-weight:600">{p.name}</span>
          <span class="mono" style="font-size:12px;color:var(--dc-dim)">{p.id}</span>
          {#if p.archivedAt}<span class="badge line">archived</span>{/if}
        </div>
        <div class="mono" style="font-size:12.5px;color:var(--dc-mut);margin-top:7px">
          {p.workingDir}
        </div>
      </a>
    {/each}
  </div>
</div>

{#if editorOpen}
  <ProjectEditor mode="create" {saving} {saveError} onSave={save} onCancel={closeEditor} />
{/if}
