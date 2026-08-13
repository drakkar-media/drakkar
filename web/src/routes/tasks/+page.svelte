<script lang="ts">
  /**
   * Displays the scheduled-task control plane: automated background jobs
   * (indexing, publishing, maintenance) driven by the backend scheduler, plus
   * manually-triggered "Operations" tasks with no scheduled counterpart.
   *
   * Schedule state polls every 30s (paused when the tab is hidden). Running
   * state and results for manual tasks are also kept live via SSE — most
   * manual tasks only queue background work, so their real outcome arrives
   * later through the backgroundKinds / backgroundResultUpdates maps below
   * rather than the initial API response.
   */
  import { onMount } from 'svelte';
  import Play from '@lucide/svelte/icons/play';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Clock3 from '@lucide/svelte/icons/clock-3';
  import CheckCircle2 from '@lucide/svelte/icons/check-circle-2';
  import AlertTriangle from '@lucide/svelte/icons/alert-triangle';
  import ChevronDown from '@lucide/svelte/icons/chevron-down';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import BackupRestorePanel from '$lib/components/BackupRestorePanel.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import * as Table from '$lib/components/ui/table/index.js';
  import { toastError, toastSuccess } from '$lib/toast';
  import { api, subscribeEvents } from '$lib/api';
  import type { TaskSchedule } from '$lib/types';

  type TaskResult = { ok: boolean; detail: string; ranAt: string };
  type TaskDef = {
    id: string;
    label: string;
    description: string;
    group: string;
    interval: string;
    manual: boolean;
    run: () => Promise<unknown>;
  };

  let running: Record<string, boolean> = {};
  let results: Record<string, TaskResult> = {};
  // Operations (manual-only actions) collapsed by default -- these are the
  // least frequently needed, and the automated groups above them already
  // show live schedule status at a glance without expanding anything.
  let collapsedGroups = new Set<string>(['Operations']);
  function toggleGroup(group: string) {
    const next = new Set(collapsedGroups);
    if (next.has(group)) next.delete(group); else next.add(group);
    collapsedGroups = next;
  }
  let schedules: TaskSchedule[] = [];
  let schedulesLoading = true;

  // Task IDs match backend task scheduler IDs (internal/app/app.go ListTaskSchedules).
  // "Operations" group = individually-triggerable via API, no corresponding scheduled entry.
  const tasks: TaskDef[] = [
    // === Indexing (automated) ===
    { id: 'seerr_sync',        label: 'Sync Seerr Requests',   description: 'Import new and updated requests from Seerr.',                                                       group: 'Indexing',   interval: '10m',  manual: true,  run: async () => { await api.syncRequests();                  return 'started in background'; } },
    { id: 'pending_queue_push',label: 'Dispatch Pending Queue', description: 'Push pending library items into the bounded background work queue.',                                group: 'Indexing',   interval: '30s',  manual: false, run: async () => '' },
    { id: 'hydra_recent_tv',   label: 'Recent TV Feed',         description: 'Fetch Hydra recent-TV RSS feed and index new TV releases.',                                         group: 'Indexing',   interval: 'RSS',  manual: false, run: async () => '' },
    { id: 'hydra_recent_movie',label: 'Recent Movie Feed',      description: 'Fetch Hydra recent-movie RSS feed and index new movie releases.',                                   group: 'Indexing',   interval: 'RSS',  manual: false, run: async () => '' },
    { id: 'queue_housekeeping',label: 'Queue Housekeeping',     description: 'Reset stuck/stale queue items then retry failed downloads. Runs every 10 min.',                    group: 'Indexing',   interval: '10m',  manual: false, run: async () => '' },
    { id: 'backlog_search',    label: 'Backlog Search',         description: 'Search missing library items — one search per show+season per batch, 1-hour cooldown per item.',   group: 'Indexing',   interval: '30m',  manual: true,  run: async () => { await api.searchPendingLibrary();          return 'started in background'; } },
    { id: 'content_maintenance',label: 'Content Maintenance',  description: 'Fill missing episode library items and run quality upgrade searches. Runs every 6 h.',              group: 'Indexing',   interval: '6h',   manual: false, run: async () => '' },
    // === Publishing (automated) ===
    { id: 'publishing_maintenance', label: 'Publishing Maintenance', description: 'Republish pending library items and reset orphaned available items. Runs every 30 min.',      group: 'Publishing', interval: '30m',  manual: false, run: async () => '' },
    // === Maintenance (automated + manual) ===
    { id: 'health_check',      label: 'Symlink Health Check',   description: 'Verify published symlinks still point to valid VFS targets.',                                       group: 'Maintenance',interval: '15m',  manual: true,  run: async () => { await api.runHealthCheck();                 return 'started in background'; } },
    { id: 'nzb_health_check',  label: 'Deep NZB Article Check', description: 'Full NNTP article scan — probes segments, resets missing-article or sample-only publications.',    group: 'Maintenance',interval: '168h', manual: false, run: async () => '' },
    { id: 'article_health_check',label: 'Article Health Check', description: 'Probe first NNTP segment of every direct-NZB item. Resets items with expired or missing articles.',group: 'Maintenance',interval: '6h',   manual: false, run: async () => '' },
    { id: 'storage_maintenance',label: 'Storage Maintenance',   description: 'Remove orphaned VFS content, broken media symlinks, and prune the block cache. Runs every 6 h.',   group: 'Maintenance',interval: '6h',   manual: false, run: async () => '' },
    // === Operations (individually-triggered via API) ===
    { id: 'retry_failed_queue',       label: 'Retry Failed Queue',          description: 'Immediately retry all failed queue items using current fallback policy.',               group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.retryFailedQueue();                    return 'started in background'; } },
    { id: 'search_upgrades',          label: 'Search Quality Upgrades',     description: 'Re-search available items whose quality profile allows a better release.',               group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.searchUpgrades();                       return 'started in background'; } },
    { id: 'fill_missing_episodes',    label: 'Fill Missing Episodes',       description: 'Use TMDB episode lists to create library items for episodes not yet tracked.',           group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.fillMissingEpisodes();        return 'started in background'; } },
    { id: 'republish_pending',        label: 'Republish Pending',           description: 'Republish library items with a selected release but no current symlink.',               group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.republishPendingLibrary();               return 'started in background'; } },
    { id: 'reset_orphaned_available', label: 'Reset Orphaned Available',    description: 'Reset available items with no symlink back to pending for re-search.',                  group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.resetOrphanedAvailableItems();           return 'started in background'; } },
    { id: 'cache_prune',              label: 'Prune Block Cache',           description: 'Delete oldest decoded articles from the disk cache.',                                   group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.pruneCache();                  return 'started in background'; } },
    { id: 'backfill_metadata',        label: 'Backfill Metadata',           description: 'Re-enrich movies and TV shows with new TMDB fields.',                                   group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.backfillMetadata();            return 'started in background'; } },
    { id: 'seerr_push_library',       label: 'Push Library to Seerr',       description: 'Push library items missing from Seerr as new requests.',                               group: 'Operations', interval: '—',    manual: true,  run: async () => { await api.pushMissingToSeerr();          return 'started in background'; } },
  ];

  async function loadSchedules() {
    try {
      schedules = (await api.taskSchedules()).items ?? [];
    } catch {
      // silently ignore — UI shows static intervals when unavailable
    } finally {
      schedulesLoading = false;
    }
  }

  /** Runs a task and records its immediate result; for queued background operations this is later overwritten with the real outcome via backgroundResultUpdates. */
  async function runTask(task: TaskDef) {
    running = { ...running, [task.id]: true };
    const ranAt = new Date().toISOString();
    try {
      const detail = String(await task.run());
      results = { ...results, [task.id]: { ok: true, detail, ranAt } };
      toastSuccess(`${task.label}: ${detail}`);
      await loadSchedules();
    } catch (err) {
      const detail = err instanceof Error ? err.message : String(err);
      results = { ...results, [task.id]: { ok: false, detail, ranAt } };
      toastError(`${task.label} failed: ${detail}`);
    } finally {
      running = { ...running, [task.id]: false };
    }
  }

  function fmtTime(iso: string) {
    return new Date(iso).toLocaleString('en-GB', { month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit' });
  }

  function scheduleFor(task: TaskDef) {
    return schedules.find((item) => item.id === task.id);
  }

  $: groups = [...new Set(tasks.map((t) => t.group))];
  $: runningCount = Object.values(running).filter(Boolean).length;
  $: lastRunCount = Object.keys(results).length;
  $: automatedCount = tasks.filter((t) => t.group !== 'Operations').length;
  $: operationsCount = tasks.filter((t) => t.group === 'Operations').length;

  // SSE event kinds from manual task triggers
  const backgroundKinds: Record<string, (e: Record<string, unknown>) => string> = {
    'library.republish_pending':    (e) => `Republish Pending: processed ${e.processed}, republished ${e.republished}`,
    'library.reset_orphaned':       (e) => `Reset Orphaned: found ${e.found}, reset ${e.reset}`,
    'library.search_pending':       (e) => `Search Pending: searched ${e.searched}, selected ${e.selected}`,
    'library.search_upgrades':      (e) => `Search Upgrades: checked ${e.checked}, upgraded ${e.upgraded}`,
    'library.push_library':         (e) => `Push to Seerr: movies ${e.moviesPushed}, shows ${e.showsPushed}`,
    'library.backfill_metadata':    (e) => `Backfill Metadata: enriched ${e.enriched ?? 0}, failed ${e.failed ?? 0}, skipped ${e.skipped ?? 0}`,
    'library.fill_missing_episodes':(e) => `Fill Episodes: processed ${e.showsProcessed} shows, created ${e.itemsCreated} items`,
    'cache.prune':                  (e) => `Cache Prune: deleted ${e.deletedFiles} files`,
    'maintenance.nzb_health_check': (e) => `NZB Health Check: scanned ${e.scannedRows}, reset ${e.resetItems}`,
    'health.check':                 (e) => `Symlink Health Check: checked ${e.checked}, healthy ${e.healthy}`,
    'queue.retry_failed':           (e) => `Retry Failed Queue: retried ${e.retried ?? 0}, failed ${e.failed ?? 0}`,
    'requests.sync':                (e) => `Sync Seerr Requests: seen ${e.seen ?? 0}, created ${e.created ?? 0}`,
  };

  // Every fire-and-forget "Operations" task's row was permanently stuck
  // showing "started in background" + a green "Success" pill the instant the
  // queue-ack resolved, because nothing ever wrote the real outcome back into
  // `results` once the background job actually finished. Maps each
  // completion event kind to the task id whose row should be updated.
  const backgroundResultUpdates: Record<string, { taskId: string; detail: (e: Record<string, unknown>) => string }> = {
    'library.republish_pending':     { taskId: 'republish_pending',        detail: (e) => `processed ${e.processed}, republished ${e.republished}, failed ${e.failed}` },
    'library.reset_orphaned':        { taskId: 'reset_orphaned_available', detail: (e) => `found ${e.found}, reset ${e.reset}, failed ${e.failed}` },
    'library.search_pending':        { taskId: 'backlog_search',           detail: (e) => `processed ${e.processed}, searched ${e.searched}, selected ${e.selected}, failed ${e.failed}` },
    'library.search_upgrades':       { taskId: 'search_upgrades',          detail: (e) => `checked ${e.checked}, upgraded ${e.upgraded}, failed ${e.failed}` },
    'library.push_library':          { taskId: 'seerr_push_library',       detail: (e) => `movies ${e.moviesPushed}, shows ${e.showsPushed}` },
    'library.backfill_metadata':     { taskId: 'backfill_metadata',        detail: (e) => `enriched ${e.enriched ?? 0}, failed ${e.failed ?? 0}, skipped ${e.skipped ?? 0}` },
    'library.fill_missing_episodes': { taskId: 'fill_missing_episodes',    detail: (e) => `processed ${e.showsProcessed} shows, created ${e.itemsCreated} items` },
    'cache.prune':                   { taskId: 'cache_prune',              detail: (e) => `deleted ${e.deletedFiles} files` },
    'health.check':                  { taskId: 'health_check',             detail: (e) => `checked ${e.checked}, healthy ${e.healthy}` },
    'queue.retry_failed':            { taskId: 'retry_failed_queue',       detail: (e) => `retried ${e.retried ?? 0}, failed ${e.failed ?? 0}` },
    'requests.sync':                 { taskId: 'seerr_sync',               detail: (e) => `seen ${e.seen ?? 0}, created ${e.created ?? 0}` },
  };

  onMount(() => {
    void loadSchedules();
    const t = window.setInterval(() => {
      if (document.visibilityState === 'visible') void loadSchedules();
    }, 30000);
    const unsub = subscribeEvents((event) => {
      if (!event) return;
      const fmt = backgroundKinds[event.kind as string];
      if (fmt) {
        toastSuccess(fmt(event));
        void loadSchedules();
      }
      const resultUpdate = backgroundResultUpdates[event.kind as string];
      if (resultUpdate) {
        results = { ...results, [resultUpdate.taskId]: { ok: true, detail: resultUpdate.detail(event), ranAt: new Date().toISOString() } };
      }
    });
    return () => { window.clearInterval(t); unsub(); };
  });
</script>

<svelte:head><title>Tasks — Drakkar</title></svelte:head>

<PageHeader title="Tasks" subtitle="Scheduled-job control plane for indexing, publishing, and maintenance work.">
  <StatusPill tone="neutral">{tasks.length} tasks</StatusPill>
  <StatusPill tone={runningCount > 0 ? 'warn' : 'ok'}>{runningCount} running</StatusPill>
</PageHeader>

<section class="mb-5 grid grid-cols-2 gap-3.5 md:grid-cols-4">
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none">{automatedCount}</div>
    <div class="mt-2 text-sm text-muted-foreground">Automated schedules</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none">{operationsCount}</div>
    <div class="mt-2 text-sm text-muted-foreground">Manual operations</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none" style={runningCount > 0 ? 'color: hsl(var(--status-warning))' : undefined}>{runningCount}</div>
    <div class="mt-2 text-sm text-muted-foreground">Currently running</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none">{lastRunCount}</div>
    <div class="mt-2 text-sm text-muted-foreground">Executed this session</div>
  </div>
</section>

<Panel title="Scheduled Tasks" subtitle="Automated tasks driven by the backend scheduler. IDs match live schedule state.">
  <Table.Root class="min-w-[880px]">
    <Table.Header>
      <Table.Row>
        <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Name</Table.Head>
        <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Interval</Table.Head>
        <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Status</Table.Head>
        <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Last Run</Table.Head>
        <Table.Head class="align-top text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Action</Table.Head>
      </Table.Row>
    </Table.Header>
    <Table.Body>
      {#each groups as group}
        {@const groupTasks = tasks.filter((t) => t.group === group)}
        {@const collapsed = collapsedGroups.has(group)}
        <Table.Row class="cursor-pointer select-none" onclick={() => toggleGroup(group)}>
          <Table.Cell colspan={5} class="bg-transparent pt-5 text-xs font-bold uppercase tracking-[0.12em] text-primary">
            <span class="inline-flex items-center gap-1.5">
              <svelte:component this={collapsed ? ChevronRight : ChevronDown} size={14} />
              {group}
              <span class="font-medium normal-case tracking-normal text-muted-foreground">{groupTasks.length}</span>
            </span>
          </Table.Cell>
        </Table.Row>
        {#if !collapsed}
        {#each groupTasks as task}
          {@const busy = running[task.id]}
          {@const result = results[task.id]}
          {@const schedule = scheduleFor(task)}
          <Table.Row>
            <Table.Cell class="whitespace-normal align-top">
              <div class="font-semibold">{task.label}</div>
              <div class="mt-2 text-sm text-muted-foreground">{task.description}</div>
              {#if result}
                <div class="mt-2.5 inline-flex items-center gap-1.5 font-mono text-xs" style={result.ok ? 'color: hsl(var(--status-available))' : 'color: hsl(var(--status-failed))'}>
                  <svelte:component this={result.ok ? CheckCircle2 : AlertTriangle} size={12} />
                  <span>{result.detail}</span>
                </div>
              {/if}
            </Table.Cell>
            <Table.Cell class="align-top text-sm text-muted-foreground">{schedule?.interval ?? task.interval}</Table.Cell>
            <Table.Cell class="align-top">
              {#if busy}
                <StatusPill tone="warn">Running</StatusPill>
              {:else if schedule?.automated}
                <StatusPill tone="ok">Automated</StatusPill>
              {:else if result?.ok}
                <StatusPill tone="ok">Success</StatusPill>
              {:else if result && !result.ok}
                <StatusPill tone="danger">Failed</StatusPill>
              {:else}
                <StatusPill tone="neutral">Idle</StatusPill>
              {/if}
            </Table.Cell>
            <Table.Cell class="whitespace-normal align-top text-sm text-muted-foreground">
              {#if result}
                <span class="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Clock3 size={12} /> {fmtTime(result.ranAt)}</span>
              {:else if schedule?.lastRunAt}
                <span class="inline-flex items-center gap-1.5 text-xs text-muted-foreground"><Clock3 size={12} /> {fmtTime(schedule.lastRunAt)}</span>
              {:else}
                <span class="inline-flex items-center gap-1.5 text-xs text-muted-foreground opacity-40">Never</span>
              {/if}
            </Table.Cell>
            <Table.Cell class="align-top">
              <Button kind="secondary" on:click={() => runTask(task)} disabled={busy || !task.manual}>
                {#if busy}
                  <RefreshCw size={14} class="animate-spin" />
                  Running…
                {:else}
                  <Play size={14} />
                  Run
                {/if}
              </Button>
            </Table.Cell>
          </Table.Row>
        {/each}
        {/if}
      {/each}
    </Table.Body>
  </Table.Root>
</Panel>

<div class="mt-5">
  <BackupRestorePanel />
</div>
