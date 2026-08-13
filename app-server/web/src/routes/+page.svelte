<script lang="ts">
  import { onMount } from "svelte"

  type Version = {
    version: string
    apiVersion: string
    commit: string
    builtAt: string
  }

  type Tailscale = {
    state: "disabled" | "starting" | "running" | "error"
    url?: string
    reason?: string
  }

  let health = $state<"checking" | "ok" | "unreachable">("checking")
  let version = $state<Version | null>(null)
  let tailscale = $state<Tailscale>({ state: "disabled" })
  let error = $state<string | null>(null)
  let checkedAt = $state<Date | null>(null)

  const refresh = async () => {
    const wasOk = health === "ok"
    try {
      const res = await fetch("/api/v1/health", { signal: AbortSignal.timeout(5000) })
      health = res.ok ? "ok" : "unreachable"
      error = res.ok ? null : `health returned ${res.status}`
    } catch (cause) {
      health = "unreachable"
      error = cause instanceof Error ? cause.message : String(cause)
    } finally {
      checkedAt = new Date()
    }
    // Refetched on recovery, not every tick: the build info only changes
    // across a server restart, and a version failure is not unhealth.
    if (health === "ok" && (version === null || !wasOk)) {
      try {
        const v = await fetch("/api/v1/version", { signal: AbortSignal.timeout(5000) })
        if (v.ok) version = await v.json()
      } catch {
        // keep the last known build info
      }
    }
    if (health === "ok") {
      try {
        const info = await fetch("/api/v1/server-info", {
          signal: AbortSignal.timeout(5000),
        })
        if (info.ok) tailscale = (await info.json()).tailscale
      } catch {
        // keep the last verified exposure state
      }
    }
  }

  onMount(() => {
    refresh()
    const timer = setInterval(refresh, 10_000)
    return () => clearInterval(timer)
  })

  const dot = $derived(health === "ok" ? "ok" : health === "checking" ? "wait" : "bad")
  const label = $derived(
    health === "ok" ? "Healthy" : health === "checking" ? "Checking…" : "Unreachable",
  )
</script>

<div class="page">
  <h1 class="h1">Server</h1>
  <p class="lede">The App Server this console is attached to.</p>

  {#if error !== null}
    <div class="errorbox">{error}</div>
  {/if}

  <div class="card">
    <div class="head"><span class="sdot {dot}"></span>{label}</div>
    <div class="rows">
      <div class="row">
        <span class="k">Endpoint</span>
        <span class="v">{location.origin}</span>
      </div>
      {#if checkedAt !== null}
        <div class="row">
          <span class="k">Last checked</span>
          <span class="v">{checkedAt.toLocaleTimeString()}</span>
        </div>
      {/if}
    </div>
  </div>

  {#if version !== null}
    <div class="card">
      <div class="head">Build</div>
      <div class="rows">
        <div class="row"><span class="k">Version</span><span class="v">{version.version}</span></div>
        <div class="row">
          <span class="k">API version</span><span class="v">{version.apiVersion}</span>
        </div>
        <div class="row"><span class="k">Commit</span><span class="v">{version.commit}</span></div>
        <div class="row"><span class="k">Built at</span><span class="v">{version.builtAt}</span></div>
      </div>
    </div>
  {/if}

  {#if tailscale.state !== "disabled"}
    <div class="card">
      <div class="head">Tailscale</div>
      <div class="rows">
        <div class="row"><span class="k">State</span><span class="v">{tailscale.state}</span></div>
        {#if tailscale.state === "running" && tailscale.url !== undefined}
          <div class="row">
            <span class="k">URL</span>
            <span class="v"><a href={tailscale.url}>{tailscale.url}</a></span>
          </div>
        {:else if tailscale.state === "error" && tailscale.reason !== undefined}
          <div class="row"><span class="k">Reason</span><span class="v">{tailscale.reason}</span></div>
        {/if}
      </div>
    </div>
  {/if}

  <p class="muted" style="margin-top: 24px">
    Refreshes every 10 seconds. The full API surface is documented in the
    <a href="/docs" style="color: var(--dc-acc)">API Reference</a>.
  </p>
</div>
