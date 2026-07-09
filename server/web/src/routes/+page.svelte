<svelte:head>
  <title>Atelier Code · Getting started</title>
</svelte:head>

<div class="pad" style="max-width:720px">
  <h1 class="h1">Getting started</h1>
  <p class="lede">
    Atelier Code is a local service, CLI, and web UI for starting and managing persistent terminal
    sessions. Here's the quickest path from zero to a running session.
  </p>

  <div style="display:flex;flex-direction:column;gap:24px;margin:30px 0 36px">
    <div class="step">
      <div class="stepn">1</div>
      <div style="flex:1;min-width:0">
        <div style="font-size:14.5px;font-weight:600">Install the toolchain</div>
        <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:4px 0 9px">
          Provisions the pinned Go, Node, and pnpm versions.
        </p>
        <div class="codeblock" style="padding:9px 12px"><span class="code">mise install</span></div>
      </div>
    </div>

    <div class="step">
      <div class="stepn">2</div>
      <div style="flex:1;min-width:0">
        <div style="font-size:14.5px;font-weight:600">Run the service</div>
        <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:4px 0 9px">
          Builds the web UI, embeds it, and serves on port 7331.
        </p>
        <div class="codeblock" style="padding:9px 12px">
          <span class="code"
            >mise run serve   <span style="color:var(--dc-dim)"># → http://127.0.0.1:7331</span></span
          >
        </div>
      </div>
    </div>

    <div class="step">
      <div class="stepn">3</div>
      <div style="flex:1;min-width:0">
        <div style="font-size:14.5px;font-weight:600">Start your first session</div>
        <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:4px 0 9px">
          From the Sessions tab, or straight from the API reference's Try it panel.
        </p>
        <div class="codeblock" style="padding:9px 12px">
          <span class="code">POST /api/sessions/start</span>
        </div>
      </div>
    </div>

    <div class="step">
      <div class="stepn">4</div>
      <div style="flex:1;min-width:0">
        <div style="font-size:14.5px;font-weight:600">Drive it</div>
        <p style="color:var(--dc-mut);font-size:13px;line-height:1.5;margin:4px 0 9px">
          Inject input as if you were typing, then attach when you want a live terminal.
        </p>
        <div class="codeblock" style="padding:9px 12px">
          <span class="code">atc sessions send-text ses_8f3a2c "hello"</span>
        </div>
      </div>
    </div>
  </div>

  <div class="seclabel">Next</div>
  <div style="display:flex;gap:14px">
    <a class="nextcard" href="/reference/start">
      <span style="font-size:13.5px;font-weight:600">Reference →</span>
      <span style="color:var(--dc-mut);font-size:12.5px"
        >Every operation, with a live Try it panel and its CLI equivalent.</span
      >
    </a>
    <a class="nextcard" href="/sessions">
      <span style="font-size:13.5px;font-weight:600">Go to Sessions →</span>
      <span style="color:var(--dc-mut);font-size:12.5px">See and manage what's running right now.</span>
    </a>
  </div>
</div>
