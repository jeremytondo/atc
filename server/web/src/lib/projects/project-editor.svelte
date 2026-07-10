<script lang="ts">
  // Slide-in editor for projects, following the ActionEditor drawer contract:
  // it owns a local draft and emits the values on save; the parent performs the
  // API call and reload so all network + error handling stays in one place.
  // In rename mode only the name is editable — a project's directory is fixed
  // after creation.
  import type { Project } from '$lib/api';

  let {
    mode,
    source = null,
    saving = false,
    saveError = '',
    onSave,
    onCancel
  }: {
    mode: 'create' | 'rename';
    source?: Project | null;
    saving?: boolean;
    saveError?: string;
    onSave: (values: { name: string; workingDir: string }) => void;
    onCancel: () => void;
  } = $props();

  // Writable deriveds: the draft starts from (and resets with) `source`;
  // user edits override it until the editor is remounted or source changes.
  let name = $derived(source?.name ?? '');
  let workingDir = $derived(source?.workingDir ?? '');

  let canSave = $derived(
    name.trim() !== '' && (mode === 'rename' || workingDir.trim() !== '')
  );

  function save() {
    if (!canSave || saving) return;
    onSave({ name: name.trim(), workingDir: workingDir.trim() });
  }
</script>

<div
  class="scrim"
  role="button"
  tabindex="-1"
  aria-label="Close editor"
  onclick={onCancel}
  onkeydown={(e) => e.key === 'Escape' && onCancel()}
></div>
<div class="drawer" role="dialog" aria-modal="true" aria-label="Project editor">
  <div class="dhead">
    <span style="font-size:15px;font-weight:600"
      >{mode === 'create' ? 'New project' : `Rename ${source?.name ?? ''}`}</span
    >
    <button class="iconbtn" style="margin-left:auto" aria-label="Close" onclick={onCancel}>✕</button>
  </div>

  <div class="dbody">
    <div style="margin-bottom:14px">
      <label class="lbl" for="prj-name">Name</label>
      <input
        id="prj-name"
        class="inp"
        value={name}
        oninput={(e) => (name = e.currentTarget.value)}
        placeholder="atc"
      />
      <p class="hint">A human label; it can be renamed later.</p>
    </div>

    <div style="margin-bottom:14px">
      <label class="lbl" for="prj-dir">Working directory</label>
      <input
        id="prj-dir"
        class="inp mono"
        value={workingDir}
        oninput={(e) => (workingDir = e.currentTarget.value)}
        placeholder="/home/you/projects/atc"
        disabled={mode === 'rename'}
      />
      {#if mode === 'rename'}
        <p class="hint">The directory is fixed after creation.</p>
      {:else}
        <p class="hint">
          Absolute path on this machine. Every session started in the project runs here.
        </p>
      {/if}
    </div>

    {#if saveError}
      <div
        class="card"
        style="border-color:color-mix(in srgb,var(--dc-red) 40%,transparent);background:color-mix(in srgb,var(--dc-red) 10%,transparent);color:var(--dc-red);padding:10px 14px;margin-top:16px;font-size:12.5px"
      >
        {saveError}
      </div>
    {/if}
  </div>

  <div class="dfoot">
    <button class="btn ghost" style="margin-left:auto" onclick={onCancel} disabled={saving}
      >Cancel</button
    >
    <button class="btn primary" class:off={!canSave} disabled={!canSave || saving} onclick={save}>
      {saving ? 'Saving…' : mode === 'create' ? 'Create project' : 'Save name'}
    </button>
  </div>
</div>
