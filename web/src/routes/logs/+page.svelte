<script lang="ts">
  import { onMount } from 'svelte';
  import Download from '@lucide/svelte/icons/download';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Search from '@lucide/svelte/icons/search';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Button from '$lib/components/Button.svelte';
  import { api } from '$lib/api';

  type LogEntry = {
    level: string;
    service: string;
    message: string;
    time: string;
    raw: string;
  };

  // Store log entries directly as parsed objects, not as raw strings.
  // This eliminates the reactive chain rawLines → parsed → filtered
  // that was not re-triggering in Svelte 5 legacy mode.
  let entries: LogEntry[] = [];
  let loading = false;
  let levelFilter = 'all';
  let term = '';
  let loadError = '';

  async function load() {
    loading = true;
    loadError = '';
    try {
      const data = await api.logs({ limit: 500, level: levelFilter !== 'all' ? levelFilter : undefined });
      const lines = data.lines ?? [];
      // Parse inline and assign a fresh array so Svelte detects the change.
      entries = lines.map(({ raw }) => {
        try {
          const obj = JSON.parse(raw);
          return {
            level:   obj.level ?? '',
            service: obj.service ?? obj.component ?? obj.module ?? '',
            message: obj.message ?? obj.msg ?? raw,
            time:    obj.time ?? '',
            raw
          };
        } catch {
          return { level: '', service: '', message: raw, time: '', raw };
        }
      });
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }

  function fmtDate(iso: string) {
    if (!iso) return '';
    try {
      return new Date(iso).toLocaleString('en-GB', {
        month: 'short', day: '2-digit',
        hour: '2-digit', minute: '2-digit', second: '2-digit'
      });
    } catch { return iso; }
  }

  function levelTone(level: string) {
    return level === 'error' ? 'error' : level === 'warn' ? 'warn' : 'default';
  }

  // Compute filtered directly in a $: statement that only depends on one signal.
  $: filtered = entries
    .filter(entry => {
      if (levelFilter !== 'all' && entry.level !== levelFilter) return false;
      if (!term) return true;
      return `${entry.service} ${entry.message} ${entry.raw}`.toLowerCase().includes(term.toLowerCase());
    })
    .sort((a, b) => b.time.localeCompare(a.time));

  onMount(() => {
    void load();
    const timer = window.setInterval(() => void load(), 30000);
    return () => window.clearInterval(timer);
  });
</script>

<svelte:head><title>Logs — Drakkar</title></svelte:head>

<PageHeader title="Logs" subtitle="Operational events assembled from backend runtime and job state.">
  <Button kind="secondary" on:click={load} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <a class="btn-link" href="/api/logs?limit=2000" target="_blank" rel="noreferrer" download>
    <Button kind="secondary">
      <Download size={14} />
      Download
    </Button>
  </a>
</PageHeader>

{#if loadError}<div class="load-error">Error: {loadError}</div>{/if}

<div class="toolbar">
  <div class="search-wrap">
    <Search size={14} class="search-icon" />
    <input bind:value={term} placeholder="Search logs, request IDs, service names…" class="search-input" />
  </div>
  <select bind:value={levelFilter} on:change={() => void load()} class="level-select">
    <option value="all">All levels</option>
    <option value="info">Info</option>
    <option value="warn">Warn</option>
    <option value="error">Error</option>
    <option value="debug">Debug</option>
  </select>
</div>

<div class="table-wrap">
  <table>
    <thead>
      <tr>
        <th class="col-time">Time</th>
        <th class="col-level">Level</th>
        <th class="col-service">Service</th>
        <th class="col-message">Message</th>
      </tr>
    </thead>
    <tbody>
      {#if loading && entries.length === 0}
        <tr><td colspan="4" class="empty">Loading…</td></tr>
      {:else if filtered.length === 0}
        <tr><td colspan="4" class="empty">No log entries match the current filter.</td></tr>
      {:else}
        {#each filtered as entry, i (i)}
          <tr class="row-{levelTone(entry.level)}">
            <td class="col-time mono muted">{fmtDate(entry.time)}</td>
            <td class="col-level">
              <span class="badge badge-{entry.level || 'default'}">{(entry.level || '?').toUpperCase()}</span>
            </td>
            <td class="col-service mono muted">{entry.service || '—'}</td>
            <td class="col-message">{entry.message}</td>
          </tr>
        {/each}
      {/if}
    </tbody>
  </table>
</div>

<style>
  .toolbar {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 12px;
    margin-bottom: 12px;
    align-items: center;
  }

  .search-wrap {
    display: flex;
    align-items: center;
    gap: 8px;
    height: 44px;
    padding: 0 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--muted-foreground));
  }

  .search-input {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: hsl(var(--foreground));
    font-size: 14px;
  }

  .search-input::placeholder { color: hsl(var(--muted-foreground)); }

  .level-select {
    height: 44px;
    padding: 0 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 13px;
    cursor: pointer;
  }

  .table-wrap {
    overflow-x: auto;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 18px;
    background: hsl(var(--background) / 0.6);
  }

  table {
    width: 100%;
    min-width: 760px;
    border-collapse: collapse;
  }

  thead { border-bottom: 1px solid hsl(0 0% 100% / 0.06); }

  th {
    padding: 12px 14px;
    text-align: left;
    font-size: 11px;
    font-weight: 600;
    text-transform: uppercase;
    letter-spacing: 0.14em;
    color: hsl(var(--muted-foreground));
    white-space: nowrap;
  }

  td {
    padding: 11px 14px;
    border-bottom: 1px solid hsl(0 0% 100% / 0.04);
    vertical-align: top;
    font-size: 13px;
  }

  tr:last-child td { border-bottom: none; }

  .col-time    { width: 140px; }
  .col-level   { width: 72px; }
  .col-service { width: 160px; }
  .col-message { min-width: 200px; }

  .mono { font-family: 'JetBrains Mono', monospace; font-size: 12px; }
  .muted { color: hsl(var(--muted-foreground)); }

  .empty {
    padding: 32px;
    text-align: center;
    color: hsl(var(--muted-foreground));
  }

  .row-error td { background: hsl(0 72% 51% / 0.06); }
  .row-warn  td { background: hsl(38 96% 55% / 0.06); }

  .badge {
    display: inline-block;
    padding: 2px 8px;
    border-radius: 8px;
    font-size: 10px;
    font-weight: 700;
    font-family: 'JetBrains Mono', monospace;
    letter-spacing: 0.06em;
  }

  .badge-error   { background: hsl(0 72% 51% / 0.2);   color: hsl(0 96% 82%); }
  .badge-warn    { background: hsl(38 96% 55% / 0.2);  color: hsl(38 100% 72%); }
  .badge-info    { background: hsl(171 82% 55% / 0.15); color: hsl(171 82% 72%); }
  .badge-debug   { background: hsl(var(--muted-foreground) / 0.15); color: hsl(var(--muted-foreground)); }
  .badge-default { background: hsl(var(--muted-foreground) / 0.15); color: hsl(var(--muted-foreground)); }

  .load-error {
    margin-bottom: 12px;
    padding: 10px 14px;
    border-radius: 12px;
    background: hsl(0 72% 51% / 0.15);
    color: hsl(0 96% 82%);
    font-size: 13px;
  }

  .btn-link { display: contents; }

  @media (max-width: 700px) {
    .toolbar { grid-template-columns: 1fr; }
  }
</style>
