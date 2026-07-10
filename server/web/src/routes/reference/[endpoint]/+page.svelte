<script lang="ts">
  import { onMount } from 'svelte';
  import { page } from '$app/state';
  import { endpointByKey, type Endpoint } from '$lib/docs/endpoints';
  import { authHeaders, messageFromError } from '$lib/api';

  type TryResponse = {
    status: number;
    ok: boolean;
    ms: string;
    body: string;
    sessionId: string;
  };

  let ep = $derived(endpointByKey(page.params.endpoint ?? ''));

  function initialValues(endpoint: Endpoint | undefined): Record<string, string> {
    const next: Record<string, string> = {};
    for (const field of endpoint?.fields ?? []) {
      next[field.key] = field.kind === 'select' ? (field.options?.[0] ?? '') : '';
    }
    return next;
  }

  const firstEndpoint = endpointByKey(page.params.endpoint ?? '');
  let values = $state<Record<string, string>>(initialValues(firstEndpoint));
  let response = $state<TryResponse | null>(null);
  let sending = $state(false);
  let copied = $state(false);
  let host = $state('127.0.0.1:7331');
  let lastKey = firstEndpoint?.key ?? '';

  // Reset the form and last response whenever the user navigates to a different
  // endpoint (SvelteKit reuses this component across [endpoint] param changes).
  $effect(() => {
    const key = ep?.key ?? '';
    if (key !== lastKey) {
      lastKey = key;
      values = initialValues(ep);
      response = null;
      sending = false;
    }
  });

  let canSend = $derived.by(() => {
    if (!ep) return false;
    return ep.fields
      .filter((field) => field.required)
      .every((field) => (values[field.key] ?? '').trim() !== '');
  });

  let requestPreview = $derived.by(() => {
    if (!ep) return { line: '', body: '', hasBody: false };
    let displayPath = ep.path;
    const body: Record<string, string> = {};
    const query = new URLSearchParams();
    for (const field of ep.fields) {
      const raw = (values[field.key] ?? '').trim();
      const token = `{${field.key}}`;
      if (ep.path.includes(token)) {
        displayPath = displayPath.replace(token, raw !== '' ? raw : token);
        continue;
      }
      if (raw === '' || raw === '(any)') continue;
      if (ep.method === 'GET') query.set(field.key, raw);
      else body[field.key] = raw;
    }
    const qs = query.toString();
    const hasBody = ep.method !== 'GET' && Object.keys(body).length > 0;
    return {
      line: `${ep.method} ${displayPath}${qs ? `?${qs}` : ''}`,
      body: hasBody ? JSON.stringify(body, null, 2) : '',
      hasBody
    };
  });

  function setField(key: string, event: Event) {
    const target = event.currentTarget as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
    values = { ...values, [key]: target.value };
  }

  async function send() {
    if (!ep || !canSend || sending) return;
    sending = true;

    let path = ep.path;
    const body: Record<string, string> = {};
    const query = new URLSearchParams();
    for (const field of ep.fields) {
      const raw = (values[field.key] ?? '').trim();
      const token = `{${field.key}}`;
      if (ep.path.includes(token)) {
        path = path.replace(token, encodeURIComponent(raw));
        continue;
      }
      if (raw === '' || raw === '(any)') continue;
      if (ep.method === 'GET') query.set(field.key, raw);
      else body[field.key] = raw;
    }
    const qs = query.toString();
    const url = path + (qs ? `?${qs}` : '');

    const headers = authHeaders();
    const init: RequestInit = { method: ep.method, headers };
    if (ep.method !== 'GET' && Object.keys(body).length > 0) {
      headers.set('Content-Type', 'application/json');
      init.body = JSON.stringify(body);
    }

    const started = performance.now();
    try {
      const res = await fetch(url, init);
      const text = await res.text();
      const ms = `${Math.round(performance.now() - started)}ms`;
      let pretty = text;
      let sessionId = '';
      try {
        const parsed = JSON.parse(text);
        pretty = JSON.stringify(parsed, null, 2);
        if (ep.key === 'start' && res.ok && parsed && typeof parsed.id === 'string') {
          sessionId = parsed.id;
        }
      } catch {
        // Non-JSON body (e.g. empty) — show the raw text as-is.
      }
      response = { status: res.status, ok: res.ok, ms, body: pretty, sessionId };
    } catch (error) {
      response = {
        status: 0,
        ok: false,
        ms: `${Math.round(performance.now() - started)}ms`,
        body: messageFromError(error),
        sessionId: ''
      };
    } finally {
      sending = false;
    }
  }

  async function copyExample(text: string) {
    try {
      await navigator.clipboard.writeText(text);
      copied = true;
      setTimeout(() => (copied = false), 1200);
    } catch {
      // Clipboard unavailable (e.g. insecure context) — silently ignore.
    }
  }

  function statusStyle(ok: boolean) {
    const color = ok ? 'var(--dc-green)' : 'var(--dc-red)';
    return `color:${color};background:color-mix(in srgb,${color} 13%,transparent);border-color:color-mix(in srgb,${color} 34%,transparent)`;
  }

  function statusText(res: TryResponse) {
    if (!res.status) return 'Network error';
    return `${res.status} ${res.ok ? 'OK' : 'Error'}`;
  }

  onMount(() => {
    host = location.host;
  });
</script>

<svelte:head>
  <title>atc · {ep ? ep.title : 'Reference'}</title>
</svelte:head>

{#if !ep}
  <div class="pad">
    <h1 class="h1">Not found</h1>
    <p class="lede">No API reference exists for “{page.params.endpoint}”.</p>
    <div style="margin-top:18px"><a class="btn" href="/reference/start">Back to reference</a></div>
  </div>
{:else}
  <div class="pad">
    <div style="display:flex;align-items:center;gap:10px;margin-bottom:13px">
      <span class="badge {ep.method !== 'GET' ? 'solid' : 'line'}">{ep.method}</span>
      <span class="mono" style="font-size:13px;color:var(--dc-mut)">{ep.path}</span>
    </div>
    <h1 class="h1">{ep.title}</h1>
    <p class="lede">{ep.desc}</p>

    <div style="display:flex;gap:34px;align-items:flex-start;margin-top:28px">
      <!-- docs main -->
      <div style="flex:1;min-width:0;max-width:600px">
        {#if ep.params.length > 0}
          <div class="seclabel">Body parameters</div>
          <div class="card" style="margin-bottom:26px">
            {#each ep.params as p (p.name)}
              <div class="row" style="align-items:flex-start">
                <div style="min-width:0">
                  <div>
                    <span class="mono" style="font-size:13px;color:var(--dc-tx)">{p.name}</span>
                    {#if p.required}<span class="req">REQUIRED</span>{/if}
                  </div>
                  <div style="color:var(--dc-mut);font-size:12.5px;line-height:1.5;margin-top:3px">
                    {p.desc}
                  </div>
                </div>
                <span class="badge">{p.type}</span>
              </div>
            {/each}
          </div>
        {/if}

        <div class="seclabel">Response · 200</div>
        <div class="codeblock"><pre class="code">{ep.returns}</pre></div>

        {#if ep.cli}
          <div class="seclabel" style="margin:26px 0 11px">From the CLI</div>
          <div class="card" style="overflow:hidden">
            <div
              style="display:flex;align-items:center;justify-content:space-between;padding:10px 14px;border-bottom:1px solid var(--dc-bd)"
            >
              <span class="mono" style="font-size:12.5px;color:var(--dc-mut)"
                ><span class="mtag" style="color:var(--dc-green)">$</span>{ep.cli.cmd}</span
              >
              <button class="btn xs" onclick={() => copyExample(ep!.cli!.example)}
                >{copied ? 'Copied' : 'Copy'}</button
              >
            </div>
            <div style="padding:12px 14px"><pre class="code">{ep.cli.example}</pre></div>
          </div>
        {/if}
      </div>

      <!-- try-it rail -->
      <div style="width:362px;flex:none;position:sticky;top:69px">
        <div class="card" style="overflow:hidden">
          <div
            style="display:flex;align-items:center;gap:9px;padding:14px 16px;border-bottom:1px solid var(--dc-bd)"
          >
            <span class="livedot"></span>
            <span style="font-weight:600;font-size:13.5px">Try it</span>
            <span class="mono" style="margin-left:auto;font-size:11px;color:var(--dc-dim)"
              >live · {host}</span
            >
          </div>
          <div style="padding:16px">
            {#each ep.fields as f (f.key)}
              <div style="margin-bottom:13px">
                <span class="lbl"
                  >{f.label}{#if f.required}<span class="req">REQUIRED</span>{/if}</span
                >
                {#if f.kind === 'select'}
                  <select class="sel" value={values[f.key] ?? ''} onchange={(e) => setField(f.key, e)}>
                    {#each f.options ?? [] as o (o)}<option value={o}>{o}</option>{/each}
                  </select>
                {:else if f.kind === 'textarea'}
                  <textarea
                    class="ta"
                    value={values[f.key] ?? ''}
                    oninput={(e) => setField(f.key, e)}
                    placeholder={f.placeholder}
                  ></textarea>
                {:else}
                  <input
                    class="inp mono"
                    value={values[f.key] ?? ''}
                    oninput={(e) => setField(f.key, e)}
                    placeholder={f.placeholder}
                  />
                {/if}
              </div>
            {/each}

            <div class="codeblock" style="margin:4px 0 14px;padding:9px 11px">
              <div class="mono" style="font-size:11px;color:var(--dc-acc);margin-bottom:3px">
                {requestPreview.line}
              </div>
              {#if requestPreview.hasBody}<pre
                  class="code"
                  style="font-size:11px;color:var(--dc-mut)">{requestPreview.body}</pre>{/if}
            </div>

            <button
              class="btn primary"
              class:off={!canSend}
              style="width:100%;height:34px"
              disabled={!canSend || sending}
              onclick={send}
            >
              {sending ? 'Sending…' : ep.key === 'start' ? 'Start session' : 'Send request'}
            </button>

            {#if response}
              <div style="margin-top:15px;border-top:1px solid var(--dc-bd);padding-top:14px">
                <div style="display:flex;align-items:center;gap:9px;margin-bottom:9px">
                  <span class="badge" style={statusStyle(response.ok)}>{statusText(response)}</span>
                  <span class="mono" style="font-size:11px;color:var(--dc-dim)">{response.ms}</span>
                </div>
                <div class="codeblock" style="padding:10px 12px">
                  <pre class="code" style="font-size:11.5px">{response.body}</pre>
                </div>
                {#if response.sessionId}
                  <a class="btn ghost" style="width:100%;margin-top:10px" href="/sessions"
                    >Open in Sessions →</a
                  >
                {/if}
              </div>
            {/if}
          </div>
        </div>
      </div>
    </div>
  </div>
{/if}
