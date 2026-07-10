<script lang="ts">
  // Slide-in editor for actions, ported from the "Action editor" drawer in
  // "Atelier Code Docs Console - Linear.dc.html". It owns a local draft built from the
  // source action and emits an ActionWrite on save; the parent performs the API
  // call and reload so all network + error handling stays in one place.
  import type { ActionDetail, ActionWrite, ParamSpec } from '$lib/api';

  type DraftParam = {
    name: string;
    type: 'enum' | 'bool';
    valuesText: string;
    flag: string;
    defaultText: string;
    // Carried through from the source action but not editable here, so saving an
    // existing action never drops its per-param display metadata.
    label: string;
    description: string;
  };

  type Draft = {
    // The human-facing name the operator types (backend `label`); the machine id
    // is derived from it, so there is only one field to fill.
    label: string;
    description: string;
    command: string;
    // One entry per argv token, mirroring the backend's `args` array. Editing
    // each arg separately preserves token boundaries — an arg with a space in it
    // stays a single argument instead of being split on save.
    args: string[];
    promptEnabled: boolean;
    promptFlag: string;
    params: DraftParam[];
    enabled: boolean;
  };

  let {
    mode,
    source,
    saving = false,
    saveError = '',
    deleteLabel = null,
    onSave,
    onDelete,
    onCancel
  }: {
    mode: 'create' | 'edit';
    source: ActionDetail | null;
    saving?: boolean;
    saveError?: string;
    deleteLabel?: string | null;
    onSave: (write: ActionWrite) => void;
    onDelete: () => void;
    onCancel: () => void;
  } = $props();

  function toDraft(d: ActionDetail | null): Draft {
    if (!d) {
      return {
        label: '',
        description: '',
        command: '',
        args: [],
        promptEnabled: false,
        promptFlag: '',
        params: [],
        enabled: true
      };
    }
    return {
      label: d.label ?? '',
      description: d.description ?? '',
      command: d.command ?? '',
      args: [...(d.args ?? [])],
      promptEnabled: !!d.prompt,
      promptFlag: d.prompt?.flag ?? '',
      params: Object.entries(d.params ?? {}).map(([name, spec]) => ({
        name,
        type: spec.type === 'bool' ? 'bool' : 'enum',
        valuesText: (spec.values ?? []).join(', '),
        flag: spec.flag ?? '',
        defaultText: spec.default === undefined || spec.default === null ? '' : String(spec.default),
        label: spec.label ?? '',
        description: spec.description ?? ''
      })),
      enabled: d.enabled ?? true
    };
  }

  function initialDraft(): Draft {
    return toDraft(source);
  }

  // Built once per mount; the parent unmounts the drawer between opens.
  let draft = $state<Draft>(initialDraft());

  // slugify mirrors the backend's action.Slugify purely to preview the id as you
  // type; the backend is authoritative and re-derives it on create, so the two
  // never need to be kept in lockstep beyond this cosmetic hint.
  function slugify(value: string): string {
    return value
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, '-')
      .replace(/^-+|-+$/g, '');
  }

  // The machine id is derived from the name on create and fixed on edit — it is
  // the immutable key the API and CLI use, so renaming only changes the label.
  let actionId = $derived(mode === 'edit' ? (source?.name ?? '') : slugify(draft.label));
  let idValid = $derived(/^[A-Za-z0-9_-]+$/.test(actionId));
  let canSave = $derived(draft.label.trim() !== '' && idValid && draft.command.trim() !== '');

  // cleanArgs drops blank rows the operator added but never filled, while
  // preserving every other arg verbatim (including internal whitespace).
  let cleanArgs = $derived(draft.args.filter((a) => a !== ''));

  let argvPreview = $derived.by(() => {
    const tokens = [draft.command || '…', ...cleanArgs];
    for (const p of draft.params) {
      if (p.type === 'enum') {
        const value = p.defaultText.trim();
        if (value) {
          if (p.flag.trim()) tokens.push(p.flag.trim());
          tokens.push(value);
        }
      } else if (p.type === 'bool') {
        if (p.defaultText.trim() === 'true' && p.flag.trim()) tokens.push(p.flag.trim());
      }
    }
    if (draft.promptEnabled) {
      if (draft.promptFlag.trim()) tokens.push(draft.promptFlag.trim());
      tokens.push('<prompt>');
    }
    // Quote tokens that contain whitespace so the preview reads as distinct argv
    // entries rather than looking like several tokens.
    return tokens.map((t) => (/\s/.test(t) ? `"${t}"` : t)).join(' ');
  });

  function addArg() {
    draft.args = [...draft.args, ''];
  }

  function removeArg(index: number) {
    draft.args = draft.args.filter((_, i) => i !== index);
  }

  function addParam() {
    draft.params = [
      ...draft.params,
      { name: '', type: 'enum', valuesText: '', flag: '', defaultText: '', label: '', description: '' }
    ];
  }

  function removeParam(index: number) {
    draft.params = draft.params.filter((_, i) => i !== index);
  }

  function buildWrite(): ActionWrite {
    const params: Record<string, ParamSpec> = {};
    for (const p of draft.params) {
      const name = p.name.trim();
      if (name === '') continue;
      const spec: ParamSpec = { type: p.type };
      const flag = p.flag.trim();
      if (flag) spec.flag = flag;
      if (p.label.trim()) spec.label = p.label.trim();
      if (p.description.trim()) spec.description = p.description.trim();
      if (p.type === 'enum') {
        spec.values = p.valuesText
          .split(',')
          .map((v) => v.trim())
          .filter((v) => v !== '');
        const def = p.defaultText.trim();
        if (def) spec.default = def;
      } else {
        const def = p.defaultText.trim();
        if (def === 'true') spec.default = true;
        else if (def === 'false') spec.default = false;
      }
      params[name] = spec;
    }
    return {
      // On create the id is left to the backend to derive from the label (the
      // single source of truth); the preview above just mirrors it. On edit the
      // id is fixed and sent so the update targets the right action.
      name: mode === 'edit' ? actionId : undefined,
      label: draft.label.trim(),
      description: draft.description.trim(),
      command: draft.command.trim(),
      args: cleanArgs,
      prompt: draft.promptEnabled ? { flag: draft.promptFlag.trim() } : null,
      params,
      enabled: draft.enabled
    };
  }

  function save() {
    if (!canSave || saving) return;
    onSave(buildWrite());
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
<div class="drawer" role="dialog" aria-modal="true" aria-label="Action editor">
  <div class="dhead">
    <span style="font-size:15px;font-weight:600"
      >{mode === 'create' ? 'New action' : `Edit ${source?.label || source?.name || ''}`}</span
    >
    <button class="iconbtn" style="margin-left:auto" aria-label="Close" onclick={onCancel}>✕</button>
  </div>

  <div class="dbody">
    <div style="margin-bottom:14px">
      <label class="lbl" for="ed-name">Name</label>
      <input
        id="ed-name"
        class="inp"
        value={draft.label}
        oninput={(e) => (draft.label = e.currentTarget.value)}
        placeholder="Claude Code"
      />
      {#if actionId}
        <p class="hint">
          API id · <span class="mono" style="color:var(--dc-mut)">{actionId}</span>{#if mode === 'edit'}
            · fixed{/if}
        </p>
      {:else}
        <p class="hint">Shown when you start a session; the API id is generated from it.</p>
      {/if}
    </div>

    <div style="margin-bottom:14px">
      <label class="lbl" for="ed-desc">Description</label>
      <input
        id="ed-desc"
        class="inp"
        value={draft.description}
        oninput={(e) => (draft.description = e.currentTarget.value)}
        placeholder="What this action runs."
      />
    </div>

    <div class="seclabel" style="margin:22px 0 12px">Launch command</div>
    <div style="margin-bottom:14px">
      <label class="lbl" for="ed-command">Command</label>
      <input
        id="ed-command"
        class="inp mono"
        value={draft.command}
        oninput={(e) => (draft.command = e.currentTarget.value)}
        placeholder="codex"
      />
      <p class="hint">The executable to run.</p>
    </div>

    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:5px">
      <div class="lbl" style="margin:0">Base arguments</div>
      <button class="btn xs" onclick={addArg}>+ Add</button>
    </div>
    <p class="hint" style="margin:0 0 10px">
      One argv token per row, always passed. Whitespace inside a row stays part of that argument.
    </p>
    {#each draft.args as arg, i (i)}
      <div style="display:flex;gap:10px;margin-bottom:8px">
        <input
          class="inp mono"
          value={arg}
          oninput={(e) => (draft.args[i] = e.currentTarget.value)}
          placeholder="-l"
          style="flex:1"
          aria-label={`Argument ${i + 1}`}
        />
        <button class="iconbtn" aria-label="Remove argument" onclick={() => removeArg(i)}>✕</button>
      </div>
    {/each}

    <div
      style="display:flex;align-items:center;gap:11px;padding:12px 0;border-top:1px solid var(--dc-bd);border-bottom:1px solid var(--dc-bd);margin-bottom:16px"
    >
      <div style="flex:1">
        <div style="font-size:13px;font-weight:500">Accepts an initial prompt</div>
        <p class="hint">Passes a starting prompt to agent CLIs.</p>
      </div>
      {#if draft.promptEnabled}
        <input
          class="inp mono"
          value={draft.promptFlag}
          oninput={(e) => (draft.promptFlag = e.currentTarget.value)}
          placeholder="flag (blank = positional)"
          style="width:180px"
        />
      {/if}
      <button
        class="switch"
        class:on={draft.promptEnabled}
        aria-label="Toggle prompt support"
        aria-pressed={draft.promptEnabled}
        onclick={() => (draft.promptEnabled = !draft.promptEnabled)}
      >
        <span class="knob"></span>
      </button>
    </div>

    <div style="display:flex;align-items:center;justify-content:space-between;margin-bottom:5px">
      <div class="seclabel" style="margin:0">Parameters</div>
      <button class="btn xs" onclick={addParam}>+ Add</button>
    </div>
    <p class="hint" style="margin:0 0 12px">
      Only <b>enum</b> and <b>bool</b> are allowed — free-form text is intentionally unsupported, so
      request data is never interpolated raw into the command.
    </p>

    {#each draft.params as p, i (i)}
      <div class="param">
        <div style="display:flex;gap:10px;margin-bottom:10px">
          <input
            class="inp mono"
            value={p.name}
            oninput={(e) => (p.name = e.currentTarget.value)}
            placeholder="param-name"
            style="flex:1"
          />
          <select
            class="sel"
            value={p.type}
            onchange={(e) => (p.type = e.currentTarget.value as 'enum' | 'bool')}
            style="width:110px"
          >
            <option value="enum">enum</option>
            <option value="bool">bool</option>
          </select>
          <button class="iconbtn" aria-label="Remove parameter" onclick={() => removeParam(i)}>✕</button>
        </div>
        {#if p.type === 'enum'}
          <div style="margin-bottom:9px">
            <label class="lbl" for={`param-values-${i}`}>Allowed values</label>
            <input
              id={`param-values-${i}`}
              class="inp mono"
              value={p.valuesText}
              oninput={(e) => (p.valuesText = e.currentTarget.value)}
              placeholder="sonnet, opus"
            />
          </div>
        {/if}
        <div class="fieldgrid">
          <div>
            <label class="lbl" for={`param-flag-${i}`}>Flag</label>
            <input
              id={`param-flag-${i}`}
              class="inp mono"
              value={p.flag}
              oninput={(e) => (p.flag = e.currentTarget.value)}
              placeholder="--model"
            />
          </div>
          <div>
            <label class="lbl" for={`param-default-${i}`}>Default</label>
            <input
              id={`param-default-${i}`}
              class="inp mono"
              value={p.defaultText}
              oninput={(e) => (p.defaultText = e.currentTarget.value)}
              placeholder={p.type === 'bool' ? 'true / false' : 'value'}
            />
          </div>
        </div>
      </div>
    {/each}

    <div class="seclabel" style="margin:22px 0 10px">Resulting command</div>
    <div class="codeblock">
      <span class="code"><span style="color:var(--dc-green)">$</span> {argvPreview}</span>
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
    {#if deleteLabel}
      <button
        class="btn"
        style="color:var(--dc-red);border-color:color-mix(in srgb,var(--dc-red) 40%,transparent)"
        onclick={onDelete}
        disabled={saving}
      >
        {deleteLabel}
      </button>
    {/if}
    <button class="btn ghost" style="margin-left:auto" onclick={onCancel} disabled={saving}
      >Cancel</button
    >
    <button class="btn primary" class:off={!canSave} disabled={!canSave || saving} onclick={save}>
      {saving ? 'Saving…' : mode === 'create' ? 'Create action' : 'Save changes'}
    </button>
  </div>
</div>
