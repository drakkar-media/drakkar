<script lang="ts">
  /**
   * Displays recent backend log lines with level/text filtering and download.
   *
   * Polls the log API every 30s while the tab is visible. Entries are parsed
   * from raw JSON log lines into plain objects up front and reassigned as a
   * fresh array on every load — this avoids a Svelte 5 legacy-mode reactivity
   * gap where a derived `rawLines → parsed → filtered` chain failed to
   * re-trigger; see inline comments below for why this shape was chosen.
   */
  import { onMount } from 'svelte';
  import Download from '@lucide/svelte/icons/download';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Search from '@lucide/svelte/icons/search';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Button from '$lib/components/Button.svelte';
  import Pagination from '$lib/components/Pagination.svelte';
  import * as Table from '$lib/components/ui/table/index.js';
  import * as Select from '$lib/components/ui/select/index.js';
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
  const pageSize = 100;
  let page = 1;
  let total = 0;
  $: totalPages = Math.max(1, Math.ceil(total / pageSize));

  async function load() {
    loading = true;
    loadError = '';
    try {
      const data = await api.logs({ page, pageSize, level: levelFilter !== 'all' ? levelFilter : undefined });
      total = data.total ?? 0;
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

  const levelLabels: Record<string, string> = {
    all: 'All levels',
    info: 'Info',
    warn: 'Warn',
    error: 'Error',
    debug: 'Debug'
  };
  $: levelFilterLabel = levelLabels[levelFilter] ?? 'All levels';

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

  function changeLevel() {
    page = 1;
    void load();
  }

  function changePage(e: CustomEvent<number>) {
    page = e.detail;
    void load();
  }

  onMount(() => {
    void load();
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void load();
    }, 30000);
    return () => window.clearInterval(timer);
  });
</script>

<svelte:head><title>Logs — Drakkar</title></svelte:head>

<PageHeader title="Logs" subtitle="Operational events assembled from backend runtime and job state.">
  <Button kind="secondary" on:click={load} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <a
    class="inline-flex min-h-9 items-center gap-2 rounded-md border border-border bg-transparent px-3.5 font-medium text-foreground transition-colors hover:bg-muted"
    href="/api/logs?limit=2000" target="_blank" rel="noreferrer" download
  >
    <Download size={14} />
    Download
  </a>
</PageHeader>

{#if loadError}<div class="mb-3 rounded-xl px-3.5 py-2.5 text-sm" style="background: hsl(var(--status-failed) / 0.15); color: hsl(var(--status-failed))">Error: {loadError}</div>{/if}

<div class="mb-3 grid grid-cols-1 items-center gap-3 sm:grid-cols-[1fr_auto]">
  <div class="flex h-11 items-center gap-2 rounded-xl border border-border bg-muted/40 px-3.5 text-muted-foreground">
    <Search size={14} />
    <input bind:value={term} placeholder="Search this page — time, service, request IDs…" class="!h-auto flex-1 !border-0 !bg-transparent p-0 text-sm text-foreground" />
  </div>
  <Select.Root type="single" bind:value={levelFilter} onValueChange={changeLevel}>
    <Select.Trigger class="!h-11 w-full">
      {levelFilterLabel}
    </Select.Trigger>
    <Select.Content>
      <Select.Item value="all">All levels</Select.Item>
      <Select.Item value="info">Info</Select.Item>
      <Select.Item value="warn">Warn</Select.Item>
      <Select.Item value="error">Error</Select.Item>
      <Select.Item value="debug">Debug</Select.Item>
    </Select.Content>
  </Select.Root>
</div>

<div class="my-2.5 flex items-center justify-between gap-3">
  <span class="text-sm text-muted-foreground">{total.toLocaleString()} matching entries</span>
  <Pagination {page} {totalPages} on:change={changePage} />
</div>

<div class="rounded-2xl border border-border bg-background/60">
  <Table.Root class="min-w-[760px]">
    <Table.Header class="border-b border-border">
      <Table.Row>
        <Table.Head class="w-35 whitespace-nowrap text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">Time</Table.Head>
        <Table.Head class="w-18 whitespace-nowrap text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">Level</Table.Head>
        <Table.Head class="w-40 whitespace-nowrap text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">Service</Table.Head>
        <Table.Head class="min-w-50 whitespace-nowrap text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">Message</Table.Head>
      </Table.Row>
    </Table.Header>
    <Table.Body>
      {#if loading && entries.length === 0}
        <Table.Row><Table.Cell colspan={4} class="p-8 text-center text-sm text-muted-foreground">Loading…</Table.Cell></Table.Row>
      {:else if filtered.length === 0}
        <Table.Row><Table.Cell colspan={4} class="p-8 text-center text-sm text-muted-foreground">No log entries match the current filter.</Table.Cell></Table.Row>
      {:else}
        {#each filtered as entry, i (i)}
          <Table.Row
            class="border-b border-white/[0.04] last:border-b-0"
            style={levelTone(entry.level) === 'error' ? 'background: hsl(var(--status-failed) / 0.06)' : levelTone(entry.level) === 'warn' ? 'background: hsl(var(--status-warning) / 0.06)' : undefined}
          >
            <Table.Cell class="w-35 whitespace-nowrap align-top font-mono text-xs text-muted-foreground">{fmtDate(entry.time)}</Table.Cell>
            <Table.Cell class="w-18 align-top">
              <span
                class="inline-block rounded-md px-2 py-0.5 font-mono text-[10px] font-bold tracking-[0.06em]"
                style={entry.level === 'error' ? 'background: hsl(var(--status-failed) / 0.2); color: hsl(var(--status-failed))'
                  : entry.level === 'warn' ? 'background: hsl(var(--status-warning) / 0.2); color: hsl(var(--status-warning))'
                  : entry.level === 'info' ? 'background: hsl(var(--status-unreleased) / 0.15); color: hsl(var(--status-unreleased))'
                  : 'background: color-mix(in oklch, var(--muted-foreground) 15%, transparent); color: var(--muted-foreground)'}
              >{(entry.level || '?').toUpperCase()}</span>
            </Table.Cell>
            <Table.Cell class="w-40 whitespace-nowrap align-top font-mono text-xs text-muted-foreground">{entry.service || '—'}</Table.Cell>
            <Table.Cell class="min-w-50 whitespace-normal align-top text-sm">{entry.message}</Table.Cell>
          </Table.Row>
        {/each}
      {/if}
    </Table.Body>
  </Table.Root>
</div>

<div class="my-2.5 flex items-center justify-between gap-3">
  <Pagination {page} {totalPages} on:change={changePage} />
</div>
