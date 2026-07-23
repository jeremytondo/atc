<script lang="ts">
  import {
    messageFromError,
    renameSession,
    type SessionDetail,
    type SessionListItem
  } from '$lib/api';
  import {
    canRenameSession,
    sessionRenameDraft,
    submitSessionRename
  } from './session-rename';

  let {
    session,
    disabled = false,
    onRenamed
  }: {
    session: SessionListItem;
    disabled?: boolean;
    onRenamed: (session: SessionDetail) => void;
  } = $props();

  let open = $state(false);
  let draft = $state('');
  let saving = $state(false);
  let saveError = $state('');
  let canSave = $derived(canRenameSession(session, draft));

  function beginRename() {
    draft = sessionRenameDraft(session);
    saveError = '';
    open = true;
  }

  function close() {
    if (saving) return;
    open = false;
    saveError = '';
  }

  async function save() {
    if (!canSave || saving) return;
    saving = true;
    saveError = '';
    try {
      const renamed = await submitSessionRename(session, draft, renameSession);
      onRenamed(renamed);
      open = false;
    } catch (e) {
      saveError = messageFromError(e);
    } finally {
      saving = false;
    }
  }
</script>

<button class="btn xs" onclick={beginRename} {disabled}>Rename</button>

{#if open}
  <div
    class="scrim"
    role="button"
    tabindex="-1"
    aria-label="Close editor"
    onclick={close}
    onkeydown={(e) => e.key === 'Escape' && close()}
  ></div>
  <div class="drawer" role="dialog" aria-modal="true" aria-label="Rename session">
    <div class="dhead">
      <span style="font-size:15px;font-weight:600">Rename session</span>
      <button class="iconbtn" style="margin-left:auto" aria-label="Close" onclick={close}>✕</button>
    </div>
    <div class="dbody">
      <label class="lbl" for={`session-name-${session.id}`}>Name</label>
      <input
        id={`session-name-${session.id}`}
        class="inp"
        value={draft}
        oninput={(e) => (draft = e.currentTarget.value)}
      />
      <p class="hint">Changes only the display name. Leave empty to clear it.</p>
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
      <button class="btn ghost" style="margin-left:auto" onclick={close} disabled={saving}>Cancel</button>
      <button class="btn primary" class:off={!canSave} disabled={!canSave || saving} onclick={save}>
        {saving ? 'Saving…' : 'Rename'}
      </button>
    </div>
  </div>
{/if}
