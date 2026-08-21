<script lang="ts">
  /**
   * Displays symlink health, library consistency, and NZB article availability.
   *
   * Shows aggregate health stats plus a filterable/paginated table of published
   * symlinks, and lets an operator trigger a background health check, republish
   * pending items, or reset orphaned "available" items. Long-running operations
   * respond immediately with a queued acknowledgement; their real results arrive
   * later via the `health.check` / `library.republish_pending` /
   * `library.reset_orphaned` SSE events handled in `onMount`, which reset the
   * corresponding in-flight flag and trigger a debounced reload.
   */
  import { onMount } from 'svelte';
  import HeartPulse from '@lucide/svelte/icons/heart-pulse';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import AlertTriangle from '@lucide/svelte/icons/alert-triangle';
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import * as Table from '$lib/components/ui/table/index.js';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { debounce } from '$lib/debounce';
  import { confirmed } from '$lib/actions';

  type HealthSummary = { total: number; checked: number; healthy: number; neverChecked: number; consistencyIssues: number; uncalibratedNZBFiles: number };
  type HealthEntry = {
    id: number;
    libraryItemId: number;
    libraryPath: string;
    targetPath: string;
    createdAt: string;
    lastCheckedAt?: string;
    healthOk?: boolean;
  };
  type HealthEntriesPage = { items: HealthEntry[]; total: number };
  type ConsistencyIssue = {
    libraryItemId: number;
    title: string;
    mediaType: string;
    queueState: string;
  };

  const PAGE_SIZE = 100;

  let summary: HealthSummary | null = null;
  let entriesPage: HealthEntriesPage = { items: [], total: 0 };
  let consistency: ConsistencyIssue[] = [];
  let loading = true;
  let checking = false;
  let republishing = false;
  let resettingOrphaned = false;

  let filter: 'all' | 'broken' | 'unchecked' = 'all';
  let page = 0;

  async function load() {
    loading = true;
    try {
      // loadEntries() doesn't depend on summary/consistency -- it only reads
      // the local filter/page state -- so it's fired alongside them instead
      // of after, rather than paying their round-trip time twice in a row.
      // It has its own internal try/catch, so its result here is unused.
      const [nextSummary, nextConsistency] = await Promise.all([
        api.healthSummary(),
        api.healthConsistency(),
        loadEntries()
      ]);
      summary = nextSummary;
      consistency = nextConsistency.items ?? [];
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function loadEntries() {
    try {
      entriesPage = await api.healthEntries({ filter, limit: PAGE_SIZE, offset: page * PAGE_SIZE });
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    }
  }

  async function setFilter(f: typeof filter) {
    filter = f;
    page = 0;
    await loadEntries();
  }

  async function goPage(delta: number) {
    page = Math.max(0, Math.min(lastPage, page + delta));
    await loadEntries();
  }

  async function runCheck() {
    checking = true;
    try {
      // Backend responds immediately with {queued: true} and scans in a
      // background goroutine — a full scan is one filesystem check per
      // published symlink (11,000+ on this library), which used to run
      // synchronously in the request and could exceed the Cloudflare proxy's
      // timeout. The real checked/healthy counts arrive via the 'health.check'
      // event handled in onMount below, which also resets `checking`.
      await api.runHealthCheck();
      toastSuccess('Health check started in background');
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
      checking = false;
    }
  }

  async function republishPending() {
    republishing = true;
    try {
      await api.republishPendingLibrary();
      toastSuccess('Republish Pending started in background');
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
      republishing = false;
    }
  }

  async function resetOrphanedAvailable() {
    if (!confirmed('Reset all orphaned available items back to missing?')) return;
    resettingOrphaned = true;
    try {
      await api.resetOrphanedAvailableItems();
      toastSuccess('Reset Orphaned Available Items started in background');
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
      resettingOrphaned = false;
    }
  }

  function fmtDate(value?: string, fallback = 'Never') {
    if (!value) return fallback;
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return fallback;
    return date.toLocaleString('en-GB', {
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    });
  }

  function shortName(path: string) {
    const parts = path.split('/');
    return parts[parts.length - 1] || path;
  }

  $: checked = summary?.checked ?? 0;
  $: healthy = summary?.healthy ?? 0;
  $: broken = checked - healthy;
  $: healthyPct = checked > 0 ? Math.round((healthy / checked) * 100) : 0;
  $: consistencyIssues = summary?.consistencyIssues ?? 0;
  $: uncalibrated = summary?.uncalibratedNZBFiles ?? 0;
  $: lastPage = Math.max(0, Math.ceil(entriesPage.total / PAGE_SIZE) - 1);
  $: pageStart = entriesPage.total === 0 ? 0 : page * PAGE_SIZE + 1;
  $: pageEnd = Math.min((page + 1) * PAGE_SIZE, entriesPage.total);

  onMount(() => {
    void load();
    const debouncedLoad = debounce(() => void load(), 500);
    return subscribeEvents((event) => {
      if (event?.kind === 'health.check') {
        toastSuccess(`Checked ${event.checked ?? 0} — ${event.healthy ?? 0} healthy`);
        checking = false;
      }
      if (event?.kind === 'library.republish_pending') {
        toastSuccess(`Republish Pending complete: processed ${event.processed}, republished ${event.republished}, failed ${event.failed}`);
        republishing = false;
      }
      if (event?.kind === 'library.reset_orphaned') {
        toastSuccess(`Reset Orphaned complete: found ${event.found}, reset ${event.reset}, failed ${event.failed}`);
        resettingOrphaned = false;
      }
      if (!checking) debouncedLoad();
    });
  });
</script>

<svelte:head><title>Health — Drakkar</title></svelte:head>

<PageHeader title="Health" subtitle="Symlink health, library consistency, and NZB article availability.">
  <Button kind="secondary" on:click={load} disabled={loading || checking}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <Button kind="primary" on:click={runCheck} disabled={loading || checking}>
    <ShieldCheck size={14} />
    {checking ? 'Running…' : 'Run Health Check'}
  </Button>
</PageHeader>

{#if summary}
  <section class="mb-4.5 grid grid-cols-2 gap-3.5 sm:grid-cols-3 xl:grid-cols-6">
    <div class="rounded-xl border border-border bg-card/80 p-4">
      <div class="text-2xl font-bold leading-none">{summary.total}</div>
      <div class="mt-2 text-sm text-muted-foreground">Total published symlinks</div>
    </div>
    <div class="rounded-xl border border-border bg-card/80 p-4">
      <div class="text-2xl font-bold leading-none" style="color: hsl(var(--status-available))">{healthy}</div>
      <div class="mt-2 text-sm text-muted-foreground">Healthy symlinks ({healthyPct}%)</div>
      <div class="mt-3 h-2 overflow-hidden rounded-full bg-muted"><div class="h-full rounded-full" style={`width:${healthyPct}%; background: hsl(var(--status-available))`}></div></div>
    </div>
    <div class="rounded-xl border border-border bg-card/80 p-4">
      <div class="text-2xl font-bold leading-none" style="color: hsl(var(--status-warning))">{summary.neverChecked}</div>
      <div class="mt-2 text-sm text-muted-foreground">Never checked</div>
    </div>
    <div class="rounded-xl border border-border bg-card/80 p-4">
      <div class="text-2xl font-bold leading-none" style="color: hsl(var(--status-failed))">{broken}</div>
      <div class="mt-2 text-sm text-muted-foreground">Broken symlinks</div>
    </div>
    <div class="rounded-xl border p-4 {consistencyIssues > 0 ? '' : 'border-border bg-card/80'}" style={consistencyIssues > 0 ? 'border-color: hsl(var(--status-failed) / 0.28); background: color-mix(in oklch, var(--card) 82%, transparent);' : undefined}>
      <div class="text-2xl font-bold leading-none" style={consistencyIssues > 0 ? 'color: hsl(var(--status-failed))' : 'color: hsl(var(--status-available))'}>{consistencyIssues}</div>
      <div class="mt-2 text-sm text-muted-foreground">Consistency issues</div>
      <div class="mt-1 text-[11px] text-muted-foreground/70">Available items with no symlink</div>
    </div>
    <div class="rounded-xl border border-border bg-card/80 p-4">
      <div class="text-2xl font-bold leading-none" style={uncalibrated > 0 ? 'color: hsl(var(--status-warning))' : 'color: hsl(var(--status-available))'}>{uncalibrated}</div>
      <div class="mt-2 text-sm text-muted-foreground">Uncalibrated NZB files</div>
      <div class="mt-1 text-[11px] text-muted-foreground/70">Pending yEnc offset correction</div>
    </div>
  </section>

  {#if summary.neverChecked > 0}
    <div class="mb-4.5 rounded-xl border px-4.5 py-4" style="border-color: hsl(var(--status-warning) / 0.28); background: hsl(var(--status-warning) / 0.08);">
      <div class="mb-2 flex items-center gap-2 font-bold" style="color: hsl(var(--status-warning))"><HeartPulse size={16} /> Attention</div>
      <ul class="m-0 list-disc pl-4.5 text-foreground">
        <li>{summary.neverChecked} item(s) have never been health-checked.</li>
        <li>Run a check now for immediate verification, or wait for the hourly background task.</li>
      </ul>
    </div>
  {/if}

  {#if consistencyIssues > 0}
    <div class="mb-4.5 rounded-xl border px-4.5 py-4" style="border-color: hsl(var(--status-failed) / 0.28); background: hsl(var(--status-failed) / 0.06);">
      <div class="mb-2 flex items-center gap-2 font-bold" style="color: hsl(var(--status-failed))"><AlertTriangle size={16} /> Consistency Issues</div>
      <p class="m-0 text-foreground">
        {consistencyIssues} library item(s) are marked <strong>available</strong> but have no published symlink.
        These items may show as available in the library but will not stream.
        Use <strong>Republish Pending</strong> when the selected release is still recoverable. Use
        <strong> Reset Orphaned Available</strong> when the item needs to be re-queued for a fresh search and download.
      </p>
      <div class="mt-3.5 flex flex-wrap gap-2.5">
        <Button kind="secondary" on:click={republishPending} disabled={loading || republishing}>
          <RefreshCw size={14} />
          {republishing ? 'Republishing…' : 'Republish Pending'}
        </Button>
        <Button kind="primary" on:click={resetOrphanedAvailable} disabled={loading || resettingOrphaned}>
          <AlertTriangle size={14} />
          {resettingOrphaned ? 'Resetting…' : 'Reset Orphaned Available'}
        </Button>
      </div>
    </div>
  {/if}

  {#if consistency.length > 0}
    <Panel title="Consistency Issues" subtitle="Items marked available but missing a published symlink.">
      <div slot="actions">
        <StatusPill tone="danger">{consistency.length} item(s)</StatusPill>
      </div>
      <Table.Root>
        <Table.Header>
          <Table.Row>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Title</Table.Head>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Type</Table.Head>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Queue State</Table.Head>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {#each consistency as issue}
            <Table.Row>
              <Table.Cell class="whitespace-normal align-top">
                <div class="font-semibold">{issue.title}</div>
                <div class="mt-2 text-sm text-muted-foreground">ID {issue.libraryItemId}</div>
              </Table.Cell>
              <Table.Cell class="align-top"><StatusPill tone="neutral">{issue.mediaType}</StatusPill></Table.Cell>
              <Table.Cell class="align-top"><StatusPill tone={issue.queueState === 'available' ? 'ok' : 'warn'}>{issue.queueState || 'unknown'}</StatusPill></Table.Cell>
            </Table.Row>
          {/each}
        </Table.Body>
      </Table.Root>
    </Panel>
  {/if}

  <Panel title="Symlink Health" subtitle="Broken and unchecked entries float to top.">
    <div slot="actions" class="flex items-center gap-2.5">
      <div class="flex gap-0.5 rounded-lg bg-muted p-0.75">
        <button class="rounded-md px-3 py-1 text-xs font-medium transition-colors {filter === 'all' ? 'bg-white/10 text-foreground' : 'text-muted-foreground hover:text-foreground'}" on:click={() => setFilter('all')}>All</button>
        <button
          class="rounded-md px-3 py-1 text-xs font-medium transition-colors {filter === 'broken' ? 'text-foreground' : 'text-muted-foreground hover:text-foreground'}"
          style={filter === 'broken' ? 'background: hsl(var(--status-failed) / 0.15); color: hsl(var(--status-failed))' : undefined}
          on:click={() => setFilter('broken')}
        >Broken</button>
        <button class="rounded-md px-3 py-1 text-xs font-medium transition-colors {filter === 'unchecked' ? 'bg-white/10 text-foreground' : 'text-muted-foreground hover:text-foreground'}" on:click={() => setFilter('unchecked')}>Unchecked</button>
      </div>
      <StatusPill tone={broken > 0 ? 'warn' : 'ok'}>{entriesPage.total} item(s)</StatusPill>
    </div>
    {#if entriesPage.items.length > 0}
      <Table.Root>
        <Table.Header>
          <Table.Row>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Name</Table.Head>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Created</Table.Head>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Last Check</Table.Head>
            <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Status</Table.Head>
          </Table.Row>
        </Table.Header>
        <Table.Body>
          {#each entriesPage.items as entry}
            <Table.Row>
              <Table.Cell class="whitespace-normal align-top">
                <div class="font-semibold">{shortName(entry.libraryPath)}</div>
                <div class="mt-2 text-sm text-muted-foreground">{entry.libraryPath}</div>
              </Table.Cell>
              <Table.Cell class="align-top">{fmtDate(entry.createdAt, 'Unknown')}</Table.Cell>
              <Table.Cell class="align-top">{fmtDate(entry.lastCheckedAt)}</Table.Cell>
              <Table.Cell class="align-top">
                {#if entry.healthOk === true}
                  <StatusPill tone="ok">Healthy</StatusPill>
                {:else if entry.healthOk === false}
                  <StatusPill tone="danger">Broken</StatusPill>
                {:else}
                  <StatusPill tone="warn">Unchecked</StatusPill>
                {/if}
              </Table.Cell>
            </Table.Row>
          {/each}
        </Table.Body>
      </Table.Root>
      {#if entriesPage.total > PAGE_SIZE}
        <div class="flex items-center justify-center gap-3.5 pb-1 pt-3.5">
          <button class="flex size-7.5 items-center justify-center rounded-lg border border-border bg-transparent text-foreground outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-30" on:click={() => goPage(-1)} disabled={page === 0} aria-label="Previous page">
            <ChevronLeft size={14} />
          </button>
          <span class="min-w-30 text-center text-sm text-muted-foreground">{pageStart}–{pageEnd} of {entriesPage.total}</span>
          <button class="flex size-7.5 items-center justify-center rounded-lg border border-border bg-transparent text-foreground outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-30" on:click={() => goPage(1)} disabled={page >= lastPage} aria-label="Next page">
            <ChevronRight size={14} />
          </button>
        </div>
      {/if}
    {:else}
      <div class="p-6 text-center text-sm text-muted-foreground">{loading ? 'Loading…' : filter === 'broken' ? 'No broken symlinks.' : filter === 'unchecked' ? 'No unchecked symlinks.' : 'No published media yet.'}</div>
    {/if}
  </Panel>

  <div class="mt-4.5 rounded-xl border border-border bg-card/80 p-5">
    <div class="mb-2 text-sm font-bold">Deep NZB Article Check</div>
    <p class="m-0 text-sm leading-relaxed text-muted-foreground">
      In addition to symlink verification, Drakkar probes NNTP providers weekly to confirm that
      the actual NNTP articles behind each published item are still available. Items whose articles
      have expired are automatically reset to <em>requested</em> so a fresh release can be selected.
      This check also runs on startup. It cannot be triggered manually — see the Tasks tab for
      the scheduled run time.
    </p>
  </div>
{/if}
