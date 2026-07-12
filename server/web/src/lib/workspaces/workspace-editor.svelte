<script lang="ts">
  // Slide-in editor for workspaces, following the ProjectEditor drawer
  // contract: it owns a local draft and emits the values on save; the parent
  // performs the API call and reload so all network + error handling stays in
  // one place. A workspace has only a name — its project is fixed at creation.
  import type { Workspace } from '$lib/api';

  let {
    mode,
    source = null,
    saving = false,
    saveError = '',
    onSave,
    onCancel
  }: {
    mode: 'create' | 'rename';
    source?: Workspace | null;
    saving?: boolean;
    saveError?: string;
    onSave: (values: { name: string }) => void;
    onCancel: () => void;
  } = $props();

  // Writable derived: the draft starts from (and resets with) `source`;
  // user edits override it until the editor is remounted or source changes.
  let name = $derived(source?.name ?? '');

  let canSave = $derived(name.trim() !== '');

  function save() {
    if (!canSave || saving) return;
    onSave({ name: name.trim() });
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
<div class="drawer" role="dialog" aria-modal="true" aria-label="Workspace editor">
  <div class="dhead">
    <span style="font-size:15px;font-weight:600"
      >{mode === 'create' ? 'New workspace' : `Rename ${source?.name ?? ''}`}</span
    >
    <button class="iconbtn" style="margin-left:auto" aria-label="Close" onclick={onCancel}>✕</button>
  </div>

  <div class="dbody">
    <div style="margin-bottom:14px">
      <label class="lbl" for="wsp-name">Name</label>
      <input
        id="wsp-name"
        class="inp"
        value={name}
        oninput={(e) => (name = e.currentTarget.value)}
        placeholder="Fix the login bug"
      />
      <p class="hint">
        A human label for one unit of work; it can be renamed later, even while archived.
      </p>
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
      {saving ? 'Saving…' : mode === 'create' ? 'Create workspace' : 'Save name'}
    </button>
  </div>
</div>
