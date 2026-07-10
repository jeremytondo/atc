<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listActions,
    getActionDetail,
    createAction,
    updateAction,
    deleteAction,
    setActionEnabled,
    messageFromError,
    type ActionOrigin,
    type ActionDetail,
    type ActionWrite,
    type ParamSpec
  } from '$lib/api';
  import ActionEditor from '$lib/actions/action-editor.svelte';
  import ErrorBanner from '$lib/error-banner.svelte';

  type ActionView = {
    name: string;
    label: string;
    origin: ActionOrigin;
    isCustom: boolean;
    isModified: boolean;
    deleteLabel: string | null;
    enabled: boolean;
    desc: string;
    acceptsPrompt: boolean;
    argv: string;
    params: { name: string; type: string }[];
    detail: ActionDetail | null;
  };

  let actions = $state<ActionView[]>([]);
  let loading = $state(false);
  let error = $state('');
  let busyName = $state('');

  let editorOpen = $state(false);
  let editorMode = $state<'create' | 'edit'>('create');
  let editorSource = $state<ActionDetail | null>(null);
  let editorDeleteLabel = $state<string | null>(null);
  let saving = $state(false);
  let saveError = $state('');

  // buildArgv mirrors the backend's launch composition well enough to preview the
  // effective command: base command + args, each param's default (enum value or a
  // bool flag when it defaults true), then the prompt placement.
  function buildArgv(d: ActionDetail | null): string {
    if (!d) return '';
    const tokens = [d.command || '…', ...(d.args ?? [])];
    for (const [, spec] of Object.entries(d.params ?? {}).sort(([a], [b]) => a.localeCompare(b))) {
      if (spec.type === 'enum') {
        if (spec.default !== undefined && spec.default !== null && spec.default !== '') {
          if (spec.flag) tokens.push(spec.flag);
          tokens.push(String(spec.default));
        }
      } else if (spec.type === 'bool') {
        if ((spec.default === true || spec.default === 'true') && spec.flag) tokens.push(spec.flag);
      }
    }
    if (d.prompt) {
      if (d.prompt.flag) tokens.push(d.prompt.flag);
      tokens.push('<prompt>');
    }
    return tokens.join(' ');
  }

  function paramBadges(params: Record<string, ParamSpec>): { name: string; type: string }[] {
    return Object.entries(params)
      .sort(([a], [b]) => a.localeCompare(b))
      .map(([name, spec]) => ({ name, type: spec.type }));
  }

  function badgeStyle(color: string): string {
    return `color:${color};background:color-mix(in srgb,${color} 12%,transparent);border-color:color-mix(in srgb,${color} 30%,transparent)`;
  }

  function deleteLabelForOrigin(origin: ActionOrigin | undefined): string | null {
    if (origin === 'custom') return 'Delete';
    if (origin === 'modified') return 'Revert to default';
    return null;
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const discovered = await listActions();
      actions = await Promise.all(
        discovered.map(async (a) => {
          let detail: ActionDetail | null = null;
          try {
            detail = await getActionDetail(a.name);
          } catch {
            // Detail unavailable — fall back to discovery metadata below.
          }
          const origin = detail?.origin ?? a.origin ?? 'builtin';
          const isCustom = origin === 'custom';
          const isModified = origin === 'modified';
          const params = detail?.params ?? a.params ?? {};
          return {
            name: a.name,
            label: (detail?.label ?? a.label) || a.name,
            origin,
            isCustom,
            isModified,
            deleteLabel: deleteLabelForOrigin(origin),
            enabled: detail?.enabled ?? a.enabled ?? true,
            desc: (detail?.description ?? a.description) ?? '',
            acceptsPrompt: detail ? !!detail.prompt : !!a.prompt,
            argv: buildArgv(detail),
            params: paramBadges(params),
            detail
          } satisfies ActionView;
        })
      );
    } catch (e) {
      error = messageFromError(e);
    } finally {
      loading = false;
    }
  }

  async function toggle(a: ActionView) {
    busyName = a.name;
    error = '';
    try {
      await setActionEnabled(a.name, !a.enabled);
      await load();
    } catch (e) {
      error = messageFromError(e);
    } finally {
      busyName = '';
    }
  }

  function openNew() {
    editorMode = 'create';
    editorSource = null;
    editorDeleteLabel = null;
    saveError = '';
    editorOpen = true;
  }

  function openEdit(a: ActionView) {
    if (!a.detail) return;
    editorMode = 'edit';
    editorSource = a.detail;
    editorDeleteLabel = a.deleteLabel;
    saveError = '';
    editorOpen = true;
  }

  function closeEditor() {
    editorOpen = false;
    saving = false;
    saveError = '';
  }

  async function save(write: ActionWrite) {
    saving = true;
    saveError = '';
    try {
      if (editorMode === 'create') await createAction(write);
      else await updateAction(write.name ?? '', write);
      await load();
      closeEditor();
    } catch (e) {
      saveError = messageFromError(e);
    } finally {
      saving = false;
    }
  }

  async function remove() {
    if (!editorSource) return;
    saving = true;
    saveError = '';
    try {
      await deleteAction(editorSource.name);
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
  <title>Atelier Code · Actions</title>
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
    The typed commands a session can run. Built-ins ship with Atelier Code; customizations live in your
    actions file.
  </p>

  <ErrorBanner message={error} />

  {#if loading && actions.length === 0}
    <p style="color:var(--dc-mut);font-size:13px;padding:12px 0">Loading actions…</p>
  {/if}

  <div style="display:flex;flex-direction:column;gap:12px">
    {#each actions as a (a.name)}
      <div class="card" style="padding:15px 17px">
        <div style="display:flex;align-items:center;gap:10px">
          <span style="font-size:14.5px;font-weight:600;{a.enabled ? '' : 'opacity:.5'}">{a.label}</span>
          <span class="mono" style="font-size:12px;color:var(--dc-dim);{a.enabled ? '' : 'opacity:.5'}"
            >{a.name}</span
          >
          {#if a.isCustom}
            <span class="badge" style={badgeStyle('var(--dc-acc)')}>Custom</span>
          {:else}
            <span class="badge" style={badgeStyle('var(--dc-mut)')}>Built-in</span>
            {#if a.isModified}
              <span class="badge" style={badgeStyle('var(--dc-amber)')}>Modified</span>
            {/if}
          {/if}
          {#if a.acceptsPrompt}<span class="badge">prompt</span>{/if}
          <div style="margin-left:auto;display:flex;align-items:center;gap:12px">
            {#if a.detail}
              <button class="btn xs" onclick={() => openEdit(a)}>Edit</button>
            {/if}
            <div style="display:flex;align-items:center;gap:7px">
              <span style="font-size:11.5px;color:var(--dc-dim);width:52px;text-align:right"
                >{a.enabled ? 'Enabled' : 'Disabled'}</span
              >
              <button
                class="switch"
                class:on={a.enabled}
                onclick={() => toggle(a)}
                disabled={busyName === a.name}
                aria-pressed={a.enabled}
                aria-label={`${a.enabled ? 'Disable' : 'Enable'} ${a.label}`}
              >
                <span class="knob"></span>
              </button>
            </div>
          </div>
        </div>
        <div style={a.enabled ? '' : 'opacity:.4'}>
          {#if a.desc}
            <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:8px 0 12px">{a.desc}</p>
          {/if}
          <div class="codeblock" style="padding:9px 12px">
            <span class="code"><span style="color:var(--dc-green)">$</span> {a.argv}</span>
          </div>
          {#if a.params.length > 0}
            <div style="display:flex;flex-wrap:wrap;gap:7px;margin-top:11px">
              {#each a.params as p (p.name)}
                <span class="badge" style="gap:6px"
                  ><span class="mono" style="color:var(--dc-tx)">{p.name}</span><span
                    style="color:var(--dc-dim)">{p.type}</span
                  ></span
                >
              {/each}
            </div>
          {/if}
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
    deleteLabel={editorDeleteLabel}
    onSave={save}
    onDelete={remove}
    onCancel={closeEditor}
  />
{/if}
