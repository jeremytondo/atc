<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listActions,
    createAction,
    updateAction,
    deleteAction,
    messageFromError,
    renderCommand,
    type Action,
    type ActionCreate,
    type ActionPatch
  } from '$lib/api';
  import ActionEditor from '$lib/actions/action-editor.svelte';
  import ErrorBanner from '$lib/error-banner.svelte';

  let actions = $state<Action[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busyId = $state('');
  let copiedId = $state('');

  let editorOpen = $state(false);
  let editorMode = $state<'create' | 'edit'>('create');
  let editorSource = $state<Action | null>(null);
  let saving = $state(false);
  let saveError = $state('');

  async function load() {
    loading = true;
    error = '';
    try {
      actions = await listActions();
    } catch (cause) {
      error = messageFromError(cause);
    } finally {
      loading = false;
    }
  }

  async function copyActionId(id: string) {
    try {
      await navigator.clipboard.writeText(id);
      copiedId = id;
      setTimeout(() => {
        if (copiedId === id) copiedId = '';
      }, 1200);
    } catch {
      copiedId = '';
    }
  }

  async function toggle(action: Action) {
    busyId = action.id;
    error = '';
    try {
      const updated = await updateAction(action.id, { enabled: !action.enabled });
      actions = actions.map((candidate) => (candidate.id === updated.id ? updated : candidate));
    } catch (cause) {
      error = messageFromError(cause);
    } finally {
      busyId = '';
    }
  }

  function openNew() {
    editorMode = 'create';
    editorSource = null;
    saveError = '';
    editorOpen = true;
  }

  function openEdit(action: Action) {
    editorMode = 'edit';
    editorSource = action;
    saveError = '';
    editorOpen = true;
  }

  function closeEditor() {
    editorOpen = false;
    saving = false;
    saveError = '';
  }

  async function save(write: ActionCreate | ActionPatch) {
    saving = true;
    saveError = '';
    try {
      if (editorMode === 'create') {
        await createAction(write as ActionCreate);
      } else if (editorSource) {
        await updateAction(editorSource.id, write as ActionPatch);
      }
      await load();
      closeEditor();
    } catch (cause) {
      saveError = messageFromError(cause);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!editorSource) return;
    if (!confirm(`Delete action "${editorSource.name}"?\n\nExisting sessions are not affected.`)) return;
    saving = true;
    saveError = '';
    try {
      await deleteAction(editorSource.id);
      actions = actions.filter((action) => action.id !== editorSource?.id);
      closeEditor();
    } catch (cause) {
      saveError = messageFromError(cause);
    } finally {
      saving = false;
    }
  }

  onMount(load);
</script>

<svelte:head>
  <title>atc · Actions</title>
</svelte:head>

<div class="pad" style="max-width:780px">
  <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:6px">
    <h1 class="h1">Actions</h1>
    <div style="display:flex;gap:8px">
      <button class="btn" onclick={load} disabled={loading}>Refresh</button>
      <button class="btn primary" onclick={openNew}>+ New action</button>
    </div>
  </div>
  <p class="lede" style="margin-bottom:22px">
    Server-wide launch recipes. Arguments are stored as literal values and action IDs never change.
  </p>

  <ErrorBanner message={error} />

  {#if loading && actions.length === 0}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading actions…</p>
  {:else if actions.length === 0}
    <div class="card" style="padding:20px;text-align:center;color:var(--dc-mut);font-size:13px">
      No actions configured.
    </div>
  {/if}

  <div style="display:flex;flex-direction:column;gap:12px">
    {#each actions as action (action.id)}
      <div class="card" style="padding:15px 17px">
        <div style="display:flex;align-items:center;gap:10px">
          <span style="font-size:14.5px;font-weight:600;{action.enabled ? '' : 'opacity:.5'}">
            {action.name}
          </span>
          {#if action.isAgent}<span class="badge">Agent</span>{/if}
          <div style="margin-left:auto;display:flex;align-items:center;gap:12px">
            <button class="btn xs" onclick={() => openEdit(action)}>Edit</button>
            <div style="display:flex;align-items:center;gap:7px">
              <span style="font-size:11.5px;color:var(--dc-dim);width:52px;text-align:right">
                {action.enabled ? 'Enabled' : 'Disabled'}
              </span>
              <button
                class="switch"
                class:on={action.enabled}
                onclick={() => toggle(action)}
                disabled={busyId === action.id}
                aria-pressed={action.enabled}
                aria-label={`${action.enabled ? 'Disable' : 'Enable'} ${action.name}`}
              >
                <span class="knob"></span>
              </button>
            </div>
          </div>
        </div>

        <div style="display:flex;align-items:center;gap:8px;margin-top:8px">
          <code class="mono" style="font-size:12px;color:var(--dc-dim)">{action.id}</code>
          <button
            class="btn xs"
            aria-label={`Copy action ID ${action.id}`}
            onclick={() => copyActionId(action.id)}
          >
            {copiedId === action.id ? 'Copied' : 'Copy ID'}
          </button>
        </div>

        <div style={action.enabled ? '' : 'opacity:.4'}>
          {#if action.description}
            <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:8px 0 12px">
              {action.description}
            </p>
          {/if}
          <div class="codeblock" style="padding:9px 12px;margin-top:12px">
            <span class="code">
              <span style="color:var(--dc-green)">$</span> {renderCommand(action.command, action.args)}
            </span>
          </div>
        </div>
      </div>
    {/each}
  </div>
</div>

{#if editorOpen}
  <ActionEditor
    mode={editorMode}
    source={editorSource}
    {saving}
    {saveError}
    onSave={save}
    onDelete={remove}
    onCancel={closeEditor}
  />
{/if}
