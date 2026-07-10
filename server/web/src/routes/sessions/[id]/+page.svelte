<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { page } from '$app/state';
  import { init, Terminal, FitAddon } from 'ghostty-web';

  // The [id] route param is always present here; '' only satisfies the
  // generic page.params typing and would just 404 the attach.
  const id = page.params.id ?? '';
  const tokenStorageKey = 'atc.apiToken';
  const attachTokenSubprotocolPrefix = 'atc.token.';

  let container: HTMLDivElement;
  let term: Terminal | undefined;
  let fit: FitAddon | undefined;
  let socket: WebSocket | undefined;
  let status = $state<'connecting' | 'connected' | 'session_ended' | 'internal_error' | 'disconnected' | 'error'>(
    'connecting'
  );
  let message = $state('');

  function attachSubprotocolForToken(token: string) {
    const bytes = new TextEncoder().encode(token);
    let binary = '';
    for (const byte of bytes) {
      binary += String.fromCharCode(byte);
    }
    return (
      attachTokenSubprotocolPrefix +
      btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '')
    );
  }

  onMount(() => {
    let disposed = false;

    function sendResize() {
      if (socket?.readyState === WebSocket.OPEN && term) {
        socket.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
      }
    }

    function onWindowResize() {
      fit?.fit();
      sendResize();
    }

    (async () => {
      try {
        // ghostty-web ships its VT engine as an inlined WASM blob, so init()
        // needs no extra asset path configuration under Vite or the Go binary.
        await init();
        if (disposed) return;

        term = new Terminal({ cursorBlink: true, fontSize: 14 });
        fit = new FitAddon();
        term.loadAddon(fit);
        term.open(container);
        fit.fit();

        const proto = location.protocol === 'https:' ? 'wss' : 'ws';
        const url = `${proto}://${location.host}/api/sessions/${encodeURIComponent(id)}/attach`;
        const token = localStorage.getItem(tokenStorageKey)?.trim();
        socket = token ? new WebSocket(url, [attachSubprotocolForToken(token)]) : new WebSocket(url);
        socket.binaryType = 'arraybuffer';

        socket.onopen = () => {
          status = 'connected';
          message = '';
          sendResize();
          term?.focus();
        };
        socket.onclose = (event) => {
          if (disposed) return;
          if (event.code === 1000 && event.reason === 'session_ended') {
            status = 'session_ended';
            message = 'session ended';
          } else if (event.code === 1011 && event.reason === 'internal_error') {
            status = 'internal_error';
            message = 'internal error';
          } else {
            status = 'disconnected';
            message = event.reason || 'disconnected';
          }
        };
        socket.onerror = () => {
          if (!disposed) {
            status = 'error';
            message = 'connection error';
          }
        };
        socket.onmessage = (event) => {
          // Terminal output arrives as binary frames; write the raw bytes through.
          if (event.data instanceof ArrayBuffer) term?.write(new Uint8Array(event.data));
        };

        // Keystrokes go as binary frames so the server can distinguish them from
        // the text/JSON resize control message.
        term.onData((data: string) => {
          if (socket?.readyState === WebSocket.OPEN) socket.send(new TextEncoder().encode(data));
        });
        term.onResize(() => sendResize());

        window.addEventListener('resize', onWindowResize);
      } catch (e) {
        if (!disposed) {
          status = 'error';
          message = e instanceof Error ? e.message : String(e);
        }
      }
    })();

    return () => {
      disposed = true;
      window.removeEventListener('resize', onWindowResize);
    };
  });

  onDestroy(() => {
    socket?.close();
    term?.dispose();
  });
</script>

<svelte:head>
  <title>Atelier Code · {id}</title>
</svelte:head>

<div class="flex h-screen w-screen flex-col bg-black">
  <header class="flex items-center justify-between gap-3 border-b border-border/60 px-4 py-2">
    <a href="/sessions" class="min-w-0 truncate font-mono text-sm text-foreground">{id}</a>
    <span
      class="shrink-0 text-xs"
      class:text-emerald-400={status === 'connected'}
      class:text-amber-300={status === 'connecting'}
      class:text-muted-foreground={status === 'session_ended' || status === 'disconnected'}
      class:text-destructive={status === 'error' || status === 'internal_error'}
    >
      {message || status}
    </span>
  </header>
  <div bind:this={container} class="min-h-0 flex-1"></div>
</div>
