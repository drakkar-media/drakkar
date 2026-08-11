<script lang="ts">
  /**
   * Displays the download queue (active pipeline jobs) and history (completed
   * + failed) in tabs, with per-item and bulk retry/blocklist/replace actions,
   * NZB upload (file or URL), and background-queue pause/resume.
   *
   * Refreshes via SSE events and a 15s visibility-gated poll fallback; both
   * are suppressed while any action button is mid-request (see `anyBusy`)
   * so a silent background refresh can't stomp an in-flight optimistic change.
   */
  import { onMount } from 'svelte';
  import LinkIcon from '@lucide/svelte/icons/link';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Pause from '@lucide/svelte/icons/pause';
  import Play from '@lucide/svelte/icons/play';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Upload from '@lucide/svelte/icons/upload';
  import Link from '@lucide/svelte/icons/link';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { runAction, confirmed } from '$lib/actions';
  import { debounce } from '$lib/debounce';
  import { ACTIVE_STATES } from '$lib/itemStatus';
  import type { QueueItem, WorkQueueStatus } from '$lib/types';

  let uploading = false;
  let nzbUrl = '';
  let addingUrl = false;

  let items: QueueItem[] = [];
  let workQueue: WorkQueueStatus = { paused: false, depth: 0 };
  let loading = true;
  let busy: Record<string, boolean> = {};
  function isBusy(key: string): boolean {
    return !!busy[key];
  }
  function setBusy(key: string, value: boolean) {
    busy = { ...busy, [key]: value };
  }
  function anyBusy(): boolean {
    return Object.values(busy).some(Boolean);
  }
  let tab: 'queue' | 'backedoff' | 'history' = 'queue';
  let queuePage = 1;
  let backedOffPage = 1;
  let historyPage = 1;
  let selectedHistoryIds = new Set<number>();

  // Terminal states for the History tab. There's no shared DONE_STATES export in
  // itemStatus.ts (only ACTIVE_STATES) — this pair already matches the backend's
  // canonical terminal set (see ListQueue's recent_history CTE in
  // internal/database/queue_repository.go), so it's kept as a local literal.
  const doneStates = ['available', 'failed'];
  const queuePageSize = 8;
  const backedOffPageSize = 8;
  const historyPageSize = 12;
  // Matches ListQueue's recent_history CTE limit (internal/database/queue_repository.go)
  // -- the backend deliberately caps history to the most recent 200 rows for
  // response-time reasons, so a count that lands exactly on this cap almost
  // certainly means older rows exist but were cut off, not that history is
  // coincidentally exactly 200 items long.
  const historyBackendCap = 200;

  // Some queue rows carry an SxxExx suffix baked into libraryTitle already
  // (however that item's title happened to be generated), others don't --
  // confirmed live: inconsistent across creation paths, not something to
  // paper over per-row. Deriving the badge from the actual episodes join
  // (seasonNumber/episodeNumber, now returned by ListQueue) instead of
  // parsing libraryTitle text means it's always correct for every episode
  // row regardless of how that title was built, and the "already has it"
  // check just avoids a redundant duplicate badge next to a title that
  // happens to already end in the same SxxExx.
  function episodeBadge(item: QueueItem): string | null {
    if (item.seasonNumber == null || item.episodeNumber == null) return null;
    const tag = `S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')}`;
    return item.libraryTitle.includes(tag) ? null : tag;
  }

  // isBackedOff: an active item the passive-resume dispatch sweep has given
  // up on for now (see workflow.dispatchBackoff) after repeated candidate
  // failures -- it won't be automatically re-dispatched until
  // dispatchBackoffUntil passes, possibly up to 24h out. These used to sit
  // mixed into the main Queue tab looking identical to genuinely
  // in-progress items ("why does this never move?"), so they get their own
  // tab instead of cluttering the active view.
  function isBackedOff(item: QueueItem): boolean {
    return !!item.dispatchBackoffUntil && Date.parse(item.dispatchBackoffUntil) > Date.now();
  }

  function formatBackoffRemaining(item: QueueItem): string {
    if (!item.dispatchBackoffUntil) return '';
    const ms = Date.parse(item.dispatchBackoffUntil) - Date.now();
    if (ms <= 0) return 'retrying soon';
    const hours = Math.floor(ms / 3_600_000);
    const minutes = Math.floor((ms % 3_600_000) / 60_000);
    if (hours >= 1) return `retrying in ${hours}h ${minutes}m`;
    return `retrying in ${Math.max(1, minutes)}m`;
  }
  $: backedOffItems = items.filter((item) => ACTIVE_STATES.includes(item.state) && isBackedOff(item));
  $: queueItems = items.filter((item) => ACTIVE_STATES.includes(item.state) && !isBackedOff(item));
  // The backend's /api/queue ordering groups rows by state first (so every
  // 'available' row sorts before every 'failed' row) and only sorts by
  // updatedAt WITHIN each state bucket -- correct for the Queue tab's
  // pipeline-stage grouping, but it means a recent 'failed' row could sort
  // far below an older 'available' row here. History needs one unified
  // "latest activity first" ordering across both terminal states, so it's
  // re-sorted by updatedAt (falling back to createdAt) here rather than
  // trusting the backend's state-bucketed order.
  $: historyItems = items
    .filter((item) => doneStates.includes(item.state))
    .slice()
    .sort((a, b) => Date.parse(b.updatedAt || b.createdAt || '') - Date.parse(a.updatedAt || a.createdAt || ''));
  $: failedItems = items.filter((item) => item.state === 'failed');
  $: failedHistoryIds = new Set(failedItems.map((item) => item.queueItemId));
  $: selectedFailedIds = Array.from(selectedHistoryIds).filter((id) => failedHistoryIds.has(id));
  $: selectedFailedCount = selectedFailedIds.length;
  $: totalSegments = queueItems.reduce((sum, item) => sum + (item.nzbSegmentCount || 0), 0);
  $: queueTotalPages = Math.max(1, Math.ceil(queueItems.length / queuePageSize));
  $: backedOffTotalPages = Math.max(1, Math.ceil(backedOffItems.length / backedOffPageSize));
  $: historyTotalPages = Math.max(1, Math.ceil(historyItems.length / historyPageSize));
  // Clamp back to the last valid page if the list shrinks out from under the
  // current page (e.g. failed items cleared/retried while on a later page).
  $: if (queuePage > queueTotalPages) queuePage = queueTotalPages;
  $: if (backedOffPage > backedOffTotalPages) backedOffPage = backedOffTotalPages;
  $: if (historyPage > historyTotalPages) historyPage = historyTotalPages;
  $: pagedQueueItems = queueItems.slice((queuePage - 1) * queuePageSize, queuePage * queuePageSize);
  $: pagedBackedOffItems = backedOffItems.slice((backedOffPage - 1) * backedOffPageSize, backedOffPage * backedOffPageSize);
  $: pagedHistoryItems = historyItems.slice((historyPage - 1) * historyPageSize, historyPage * historyPageSize);
  $: queueRangeStart = queueItems.length ? (queuePage - 1) * queuePageSize + 1 : 0;
  $: queueRangeEnd = Math.min(queuePage * queuePageSize, queueItems.length);
  $: backedOffRangeStart = backedOffItems.length ? (backedOffPage - 1) * backedOffPageSize + 1 : 0;
  $: backedOffRangeEnd = Math.min(backedOffPage * backedOffPageSize, backedOffItems.length);
  $: historyRangeStart = historyItems.length ? (historyPage - 1) * historyPageSize + 1 : 0;
  $: historyRangeEnd = Math.min(historyPage * historyPageSize, historyItems.length);
  $: visibleFailedHistoryIds = pagedHistoryItems.filter((item) => item.state === 'failed').map((item) => item.queueItemId);
  $: visibleFailedSelectedCount = visibleFailedHistoryIds.filter((id) => selectedHistoryIds.has(id)).length;

  async function load() {
    loading = true;
    try {
      const queue = await api.queue();
      const fresh = queue.items ?? [];
      workQueue = queue.workQueue ?? { paused: false, depth: 0 };
      retainFailedSelections(fresh);
      items = fresh;
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  // Silent background refresh (SSE-triggered or polled). Unlike load(), this
  // keeps existing rows in their current on-screen order — updating them in
  // place from the fresh data and appending only genuinely new rows — rather
  // than snapping to the server's order, which would reflow/reorder the list
  // out from under the user while they're reading it.
  async function refreshItems() {
    try {
      const queue = await api.queue();
      const fresh = queue.items ?? [];
      workQueue = queue.workQueue ?? workQueue;
      retainFailedSelections(fresh);
      const freshMap = new Map(fresh.map((i) => [i.queueItemId, i]));
      const existingIds = new Set(items.map((i) => i.queueItemId));
      const updated = items
        .filter((i) => freshMap.has(i.queueItemId))
        .map((i) => freshMap.get(i.queueItemId)!);
      const added = fresh.filter((i) => !existingIds.has(i.queueItemId));
      items = [...updated, ...added];
    } catch {
      // ignore background refresh errors
    }
  }

  async function toggleQueuePause() {
    await runAction(() => (workQueue.paused ? api.resumeQueue() : api.pauseQueue()), {
      setWorking: (v) => setBusy('toggle-pause', v),
      successMessage: (result) => {
        workQueue = result;
        return result.paused ? 'Background queue paused' : 'Background queue resumed';
      },
      afterSuccess: load
    });
  }

  async function retryItem(id: number) {
    await runAction(() => api.retryQueue(id), {
      setWorking: (v) => setBusy(`retry-${id}`, v),
      successMessage: () => 'Retry queued',
      afterSuccess: load
    });
  }

  async function pauseItem(id: number) {
    await runAction(() => api.pauseQueueItem(id), {
      setWorking: (v) => setBusy(`pause-${id}`, v),
      successMessage: () => 'Item paused',
      afterSuccess: load
    });
  }

  async function resumeItem(id: number) {
    await runAction(() => api.resumeQueueItem(id), {
      setWorking: (v) => setBusy(`pause-${id}`, v),
      successMessage: () => 'Item resumed',
      afterSuccess: load
    });
  }

  async function removeItem(id: number) {
    if (!confirmed('Remove this item from the queue? It will stop being searched/downloaded until requested again.')) return;
    await runAction(() => api.queueAction(id, 'remove'), {
      setWorking: (v) => setBusy(`remove-${id}`, v),
      successMessage: () => 'Removed from queue',
      afterSuccess: load
    });
  }

  async function clearFailed() {
    if (!confirmed('Clear all failed queue items? This removes their retry history.')) return;
    await runAction(() => api.clearFailedQueue(), {
      setWorking: (v) => setBusy('clear-failed', v),
      successMessage: (result) => `Cleared ${result.cleared} failed item${result.cleared === 1 ? '' : 's'}`,
      afterSuccess: load
    });
  }

  async function manageFailedItem(id: number, action: 'remove' | 'remove_and_blocklist' | 'remove_blocklist_and_search') {
    const messages: Record<typeof action, string> = {
      remove: 'Remove this failed queue item from history and retry state?',
      remove_and_blocklist: 'Remove this failed queue item and add its release to the runtime blocklist?',
      remove_blocklist_and_search: 'Remove this failed queue item, blocklist its release, and search for a replacement now?'
    };
    if (!confirmed(messages[action])) return;
    await runAction(() => api.queueAction(id, action), {
      setWorking: (v) => setBusy(`manage-${id}`, v),
      successMessage: (result) => result.action.replaceAll('_', ' '),
      afterSuccess: load
    });
  }

  async function manageSelectedFailed(action: 'remove' | 'remove_and_blocklist' | 'remove_blocklist_and_search') {
    if (selectedFailedIds.length === 0) return;
    const messages: Record<typeof action, string> = {
      remove: `Remove ${selectedFailedIds.length} selected failed queue item${selectedFailedIds.length === 1 ? '' : 's'} from history and retry state?`,
      remove_and_blocklist: `Remove and blocklist ${selectedFailedIds.length} selected failed queue item${selectedFailedIds.length === 1 ? '' : 's'}?`,
      remove_blocklist_and_search: `Remove, blocklist, and re-search ${selectedFailedIds.length} selected failed queue item${selectedFailedIds.length === 1 ? '' : 's'}?`
    };
    if (!confirmed(messages[action])) return;
    await runAction(() => api.bulkQueueAction(selectedFailedIds, action), {
      setWorking: (v) => setBusy(`manage-selected-${action}`, v),
      successMessage: (result) => `${result.retried ?? 0} handled, ${result.failed ?? 0} failed`,
      afterSuccess: async () => {
        selectedHistoryIds = new Set();
        await load();
      }
    });
  }

  function stageProgress(state: string): number {
    return ({
      requested: 0,
      searching: 10,
      ranking: 20,
      selected: 30,
      fetching_nzb: 45,
      indexing: 60,
      preflight: 75,
      publishing: 90,
      available: 100,
      failed: 0
    } as Record<string, number>)[state] ?? 0;
  }

  function stageLabel(item: QueueItem) {
    const labels: Record<string, string> = {
      requested: 'Waiting to search',
      searching: 'Searching indexers',
      ranking: 'Ranking releases',
      selected: 'Release selected',
      fetching_nzb: 'Fetching NZB',
      indexing: 'Indexing segments',
      preflight: 'Preflight check',
      publishing: 'Publishing library link',
      available: 'Available'
    };
    if (item.state === 'failed') return humanize(item.failureReason || 'failed');
    return labels[item.state] || humanize(item.state);
  }

  function humanize(value: string) {
    return value.replaceAll('_', ' ');
  }

  /** Drops any checked history selections whose queue item is no longer in the failed set (e.g. it was retried or cleared), keeping the checkbox state in sync with the data. */
  function retainFailedSelections(nextItems: QueueItem[]) {
    const failedIds = new Set(nextItems.filter((item) => item.state === 'failed').map((item) => item.queueItemId));
    selectedHistoryIds = new Set(Array.from(selectedHistoryIds).filter((id) => failedIds.has(id)));
  }

  function toggleHistorySelection(queueItemId: number, checked: boolean) {
    const next = new Set(selectedHistoryIds);
    if (checked) next.add(queueItemId);
    else next.delete(queueItemId);
    selectedHistoryIds = next;
  }

  function toggleVisibleFailedSelection(checked: boolean) {
    const next = new Set(selectedHistoryIds);
    for (const queueItemId of visibleFailedHistoryIds) {
      if (checked) next.add(queueItemId);
      else next.delete(queueItemId);
    }
    selectedHistoryIds = next;
  }

  onMount(() => {
    void load();
    const debouncedRefresh = debounce(() => void refreshItems(), 500);
    const unsub = subscribeEvents((event) => {
      if (event?.kind === 'library.search_pending') {
        const e = event as Record<string, unknown>;
        toastSuccess(`Search Pending complete: processed ${e.processed}, selected ${e.selected}`);
      }
      if (event?.kind === 'queue.retry_failed') {
        const e = event as Record<string, unknown>;
        toastSuccess(`Retry Failed complete: retried ${e.retried ?? 0}, failed ${e.failed ?? 0}`);
      }
      if (event?.kind === 'queue.failed_action') {
        const e = event as Record<string, unknown>;
        toastSuccess(`${String(e.action).replaceAll('_', ' ')} complete: ${e.retried ?? 0} handled, ${e.failed ?? 0} failed`);
      }
      if (!anyBusy()) debouncedRefresh();
    });
    // Poll fallback in case an SSE event is missed; skipped while any action
    // is in flight or the tab is backgrounded.
    const timer = window.setInterval(() => {
      if (!anyBusy() && document.visibilityState === 'visible') void refreshItems();
    }, 15000);
    return () => {
      window.clearInterval(timer);
      unsub();
    };
  });
</script>

<svelte:head><title>Downloads — Drakkar</title></svelte:head>

<PageHeader title="Downloads" subtitle="Queue, history, and active NZB processing in one place.">
  <Button kind="secondary" on:click={load} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <Button kind="secondary" on:click={toggleQueuePause} disabled={loading || isBusy('toggle-pause')}>
    {#if workQueue.paused}
      <Play size={14} />
      Resume Queue
    {:else}
      <Pause size={14} />
      Pause Queue
    {/if}
  </Button>
  {#if failedItems.length > 0}
    <Button kind="danger" on:click={clearFailed} disabled={isBusy('clear-failed')}>
      <Trash2 size={14} />
      Clear Failed
    </Button>
  {/if}
  <label
    class="inline-flex min-h-9 cursor-pointer items-center gap-1.5 rounded-md border border-border bg-transparent px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted {uploading ? 'cursor-default opacity-55' : ''}"
    title="Upload NZB file"
  >
    <Upload size={14} />
    {uploading ? 'Uploading…' : 'Upload NZB'}
    <input
      type="file"
      accept=".nzb,application/x-nzb,application/xml,text/xml"
      class="hidden"
      disabled={uploading}
      on:change={async (e) => {
        const input = e.currentTarget as HTMLInputElement;
        const file = input.files?.[0];
        if (!file) return;
        await runAction(() => api.addNzb(file), {
          setWorking: (v) => (uploading = v),
          successMessage: () => `${file.name} queued`,
          afterSuccess: load
        });
        input.value = '';
      }}
    />
  </label>
</PageHeader>

<section class="mb-5 grid grid-cols-2 gap-3.5 sm:grid-cols-3">
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none">{queueItems.length}</div>
    <div class="mt-2 text-sm text-muted-foreground">Active queue jobs</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none">{historyItems.length}</div>
    <div class="mt-2 text-sm text-muted-foreground">History rows</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-2xl font-bold leading-none">{failedItems.length}</div>
    <div class="mt-2 text-sm text-muted-foreground">Failed items</div>
  </div>
</section>

<form class="mb-4 flex items-center gap-2.5" on:submit|preventDefault={async () => {
  const url = nzbUrl.trim();
  if (!url) return;
  await runAction(() => api.addNzbUrl(url), {
    setWorking: (v) => (addingUrl = v),
    successMessage: () => 'NZB queued from URL',
    afterSuccess: async () => {
      nzbUrl = '';
      await load();
    }
  });
}}>
  <div class="flex h-10 flex-1 items-center gap-2.5 rounded-xl border border-border bg-muted/40 px-3.5 text-muted-foreground">
    <Link size={14} />
    <input bind:value={nzbUrl} type="url" placeholder="Paste NZB URL to import…" class="!h-auto flex-1 !border-0 !bg-transparent p-0 text-sm text-foreground" disabled={addingUrl} />
  </div>
  <Button kind="secondary" disabled={!nzbUrl.trim() || addingUrl}>
    {addingUrl ? 'Adding…' : 'Add NZB URL'}
  </Button>
</form>

<div class="mb-3 mt-5 flex gap-1.5">
  <button
    class="min-h-9 rounded-md border border-border px-3.5 text-sm font-bold lowercase transition-colors {tab === 'queue' ? 'bg-primary border-primary text-primary-foreground' : 'bg-transparent text-muted-foreground hover:bg-muted'}"
    on:click={() => (tab = 'queue')}
  >queue</button>
  <button
    class="min-h-9 rounded-md border border-border px-3.5 text-sm font-bold lowercase transition-colors {tab === 'backedoff' ? 'bg-primary border-primary text-primary-foreground' : 'bg-transparent text-muted-foreground hover:bg-muted'}"
    on:click={() => (tab = 'backedoff')}
  >
    backed off{#if backedOffItems.length} ({backedOffItems.length}){/if}
  </button>
  <button
    class="min-h-9 rounded-md border border-border px-3.5 text-sm font-bold lowercase transition-colors {tab === 'history' ? 'bg-primary border-primary text-primary-foreground' : 'bg-transparent text-muted-foreground hover:bg-muted'}"
    on:click={() => (tab = 'history')}
  >history</button>
</div>

<Panel
  title={tab === 'queue' ? 'Queue' : tab === 'backedoff' ? 'Backed Off' : 'History'}
  subtitle={tab === 'queue'
    ? 'Active lifecycle rows from request to publication.'
    : tab === 'backedoff'
      ? 'Items the automatic search gave up on for now after many failed candidates — not stuck, just parked until the backoff below expires (or Retry them now).'
      : historyItems.length >= historyBackendCap
        ? `Completed and failed rows — showing the most recent ${historyBackendCap}, older rows are trimmed.`
        : 'Completed and failed rows.'}
>
  <div slot="actions">
    <StatusPill tone="neutral">
      {tab === 'queue' ? `${queueItems.length} active` : tab === 'backedoff' ? `${backedOffItems.length} rows` : `${historyItems.length} rows`}
    </StatusPill>
    <StatusPill tone={workQueue.paused ? 'warn' : 'ok'}>{workQueue.paused ? 'Dispatch paused' : 'Dispatch running'}</StatusPill>
  </div>

  {#if tab === 'queue'}
    {#if queueItems.length === 0 && !loading}
      <div class="mt-2 text-sm text-muted-foreground">Queue empty. Nothing currently processing.</div>
    {:else}
      <div class="mb-3.5 flex items-center justify-between gap-3 text-sm text-muted-foreground max-sm:flex-col max-sm:items-start">
        <div>Showing {queueRangeStart}-{queueRangeEnd} of {queueItems.length}</div>
        <div class="inline-flex items-center gap-2">
          <button type="button" class="min-h-8 rounded-[8px] border border-border bg-transparent px-2.5 text-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45" on:click={() => (queuePage = Math.max(1, queuePage - 1))} disabled={queuePage === 1}>Prev</button>
          <span>{queuePage}/{queueTotalPages}</span>
          <button type="button" class="min-h-8 rounded-[8px] border border-border bg-transparent px-2.5 text-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45" on:click={() => (queuePage = Math.min(queueTotalPages, queuePage + 1))} disabled={queuePage === queueTotalPages}>Next</button>
        </div>
      </div>
      <div class="grid gap-2.5">
        {#each pagedQueueItems as item (item.queueItemId)}
          {@const pct = stageProgress(item.state)}
          <div class="rounded-xl border border-border bg-card/80 px-4.5 py-4">
            <div class="flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-start">
              <div class="min-w-0 overflow-hidden">
                <div class="truncate font-semibold">{item.libraryTitle}</div>
                <div class="mt-2 truncate text-sm text-muted-foreground">
                  {item.nzbFileName ? `${item.nzbFileName} · ` : ''}{item.nzbSegmentCount} segments
                </div>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                {#if item.onHold}
                  <Button kind="secondary" on:click={() => resumeItem(item.queueItemId)} disabled={isBusy(`pause-${item.queueItemId}`)}>
                    <Play size={14} />
                    Resume
                  </Button>
                {:else}
                  <Button kind="secondary" on:click={() => pauseItem(item.queueItemId)} disabled={isBusy(`pause-${item.queueItemId}`)}>
                    <Pause size={14} />
                    Pause
                  </Button>
                {/if}
                <Button kind="secondary" on:click={() => retryItem(item.queueItemId)} disabled={isBusy(`retry-${item.queueItemId}`)}>
                  <RotateCcw size={14} />
                  Retry
                </Button>
                <Button kind="secondary" on:click={() => removeItem(item.queueItemId)} disabled={isBusy(`remove-${item.queueItemId}`)}>
                  <Trash2 size={14} />
                  Remove
                </Button>
              </div>
            </div>
            <div class="my-3.5 h-2 overflow-hidden rounded-full bg-muted"><div class="h-full rounded-full bg-primary transition-[width] duration-400" style={`width:${item.onHold ? 0 : pct}%`}></div></div>
            <div class="flex items-center justify-between text-sm text-muted-foreground">
              <span>{item.onHold ? 'Paused' : stageLabel(item)}</span>
              <span class="font-mono">{item.onHold ? '' : `${pct}%`}</span>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {:else if tab === 'backedoff'}
    {#if backedOffItems.length === 0 && !loading}
      <div class="mt-2 text-sm text-muted-foreground">Nothing backed off right now.</div>
    {:else}
      <div class="mb-3.5 flex items-center justify-between gap-3 text-sm text-muted-foreground max-sm:flex-col max-sm:items-start">
        <div>Showing {backedOffRangeStart}-{backedOffRangeEnd} of {backedOffItems.length}</div>
        <div class="inline-flex items-center gap-2">
          <button type="button" class="min-h-8 rounded-[8px] border border-border bg-transparent px-2.5 text-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45" on:click={() => (backedOffPage = Math.max(1, backedOffPage - 1))} disabled={backedOffPage === 1}>Prev</button>
          <span>{backedOffPage}/{backedOffTotalPages}</span>
          <button type="button" class="min-h-8 rounded-[8px] border border-border bg-transparent px-2.5 text-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45" on:click={() => (backedOffPage = Math.min(backedOffTotalPages, backedOffPage + 1))} disabled={backedOffPage === backedOffTotalPages}>Next</button>
        </div>
      </div>
      <div class="grid gap-2.5">
        {#each pagedBackedOffItems as item (item.queueItemId)}
          <div class="rounded-xl border px-4.5 py-4" style="border-color: hsl(var(--status-warning) / 0.28); background: hsl(var(--status-warning) / 0.05);">
            <div class="flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-start">
              <div class="min-w-0 overflow-hidden">
                <div class="truncate font-semibold">
                  {item.libraryTitle}{#if episodeBadge(item)}<span class="ml-2 rounded-[4px] bg-muted-foreground/15 px-1.5 py-0.5 align-middle text-[0.7rem] font-semibold tracking-wide text-muted-foreground">{episodeBadge(item)}</span>{/if}
                </div>
                <div class="mt-2 truncate text-sm text-muted-foreground">
                  {item.dispatchAttemptCount ?? 0} candidate{(item.dispatchAttemptCount ?? 0) === 1 ? '' : 's'} tried · {formatBackoffRemaining(item)}
                </div>
              </div>
              <div class="flex shrink-0 items-center gap-2">
                <Button kind="secondary" on:click={() => retryItem(item.queueItemId)} disabled={isBusy(`retry-${item.queueItemId}`)}>
                  <RotateCcw size={14} />
                  Retry now
                </Button>
                <Button kind="secondary" on:click={() => removeItem(item.queueItemId)} disabled={isBusy(`remove-${item.queueItemId}`)}>
                  <Trash2 size={14} />
                  Remove
                </Button>
              </div>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {:else}
    {#if historyItems.length === 0 && !loading}
      <div class="mt-2 text-sm text-muted-foreground">No history yet.</div>
    {:else}
      {#if failedItems.length > 0}
        <div class="mb-3.5 flex items-center justify-between gap-3 rounded-2xl border border-border bg-muted/30 px-3.5 py-3 max-sm:flex-col max-sm:items-start">
          <label class="flex items-center gap-2.5 text-sm text-muted-foreground">
            <input
              type="checkbox"
              checked={visibleFailedHistoryIds.length > 0 && visibleFailedSelectedCount === visibleFailedHistoryIds.length}
              disabled={visibleFailedHistoryIds.length === 0 || isBusy('manage-selected-remove_and_blocklist') || isBusy('manage-selected-remove_blocklist_and_search')}
              on:change={(e) => toggleVisibleFailedSelection((e.currentTarget as HTMLInputElement).checked)}
            />
            <span>Select visible failed ({visibleFailedHistoryIds.length})</span>
          </label>
          <div class="flex items-center gap-2.5">
            <StatusPill tone="neutral">{selectedFailedCount} selected</StatusPill>
            <Button kind="secondary" on:click={() => manageSelectedFailed('remove_and_blocklist')} disabled={isBusy('manage-selected-remove_and_blocklist') || selectedFailedCount === 0}>
              <Trash2 size={14} />
              Blocklist Selected
            </Button>
            <Button kind="secondary" on:click={() => manageSelectedFailed('remove_blocklist_and_search')} disabled={isBusy('manage-selected-remove_blocklist_and_search') || selectedFailedCount === 0}>
              <SearchCheck size={14} />
              Replace Selected
            </Button>
            <Button kind="secondary" on:click={() => (selectedHistoryIds = new Set())} disabled={selectedFailedCount === 0}>
              Clear Selection
            </Button>
          </div>
        </div>
      {/if}
      <div class="mb-3.5 flex items-center justify-between gap-3 text-sm text-muted-foreground max-sm:flex-col max-sm:items-start">
        <div>Showing {historyRangeStart}-{historyRangeEnd} of {historyItems.length}</div>
        <div class="inline-flex items-center gap-2">
          <button type="button" class="min-h-8 rounded-[8px] border border-border bg-transparent px-2.5 text-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45" on:click={() => (historyPage = Math.max(1, historyPage - 1))} disabled={historyPage === 1}>Prev</button>
          <span>{historyPage}/{historyTotalPages}</span>
          <button type="button" class="min-h-8 rounded-[8px] border border-border bg-transparent px-2.5 text-foreground outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45" on:click={() => (historyPage = Math.min(historyTotalPages, historyPage + 1))} disabled={historyPage === historyTotalPages}>Next</button>
        </div>
      </div>
      <div class="grid gap-2.5">
        {#each pagedHistoryItems as item (item.queueItemId)}
          <div class="rounded-xl border px-4.5 py-4 {item.state === 'failed' ? '' : 'border-border bg-card/80'}" style={item.state === 'failed' ? 'border-color: hsl(var(--danger) / 0.28); background: hsl(var(--danger) / 0.05);' : undefined}>
            <div class="flex items-center justify-between gap-3 max-sm:flex-col max-sm:items-start">
              <div class="flex min-w-0 flex-1 items-center gap-2.5">
                {#if item.state === 'failed'}
                  <label class="flex shrink-0 items-center">
                    <input
                      type="checkbox"
                      checked={selectedHistoryIds.has(item.queueItemId)}
                      disabled={isBusy('manage-selected-remove_and_blocklist') || isBusy('manage-selected-remove_blocklist_and_search')}
                      on:change={(e) => toggleHistorySelection(item.queueItemId, (e.currentTarget as HTMLInputElement).checked)}
                    />
                  </label>
                {/if}
                <div class="min-w-0 overflow-hidden">
                  <div class="truncate font-semibold">
                    {item.libraryTitle}{#if episodeBadge(item)}<span class="ml-2 rounded-[4px] bg-muted-foreground/15 px-1.5 py-0.5 align-middle text-[0.7rem] font-semibold tracking-wide text-muted-foreground">{episodeBadge(item)}</span>{/if}
                  </div>
                  <div class="mt-2 truncate text-sm text-muted-foreground">
                    {item.nzbFileName ? `${item.nzbFileName} · ` : ''}{item.nzbSegmentCount} segments
                  </div>
                </div>
              </div>
              <div class="flex shrink-0 items-center justify-between gap-2">
                {#if item.state === 'available' && item.libraryItemId}
                  <a href={`/library/${item.libraryItemId}`} class="inline-flex min-h-9 items-center gap-1.5 rounded-md border border-border bg-muted/40 px-3 text-sm">
                    <LinkIcon size={14} />
                    Open
                  </a>
                {/if}
                {#if item.state === 'failed'}
                  <Button kind="secondary" on:click={() => retryItem(item.queueItemId)} disabled={isBusy(`retry-${item.queueItemId}`)}>
                    <RotateCcw size={14} />
                    Retry
                  </Button>
                  <Button kind="secondary" on:click={() => manageFailedItem(item.queueItemId, 'remove_and_blocklist')} disabled={isBusy(`manage-${item.queueItemId}`)}>
                    <Trash2 size={14} />
                    Blocklist
                  </Button>
                  <Button kind="secondary" on:click={() => manageFailedItem(item.queueItemId, 'remove_blocklist_and_search')} disabled={isBusy(`manage-${item.queueItemId}`)}>
                    <SearchCheck size={14} />
                    Replace
                  </Button>
                {/if}
              </div>
            </div>
            <div class="mt-3.5 text-sm text-muted-foreground">
              <StatusPill tone={item.state === 'available' ? 'ok' : 'danger'}>
                {item.state === 'available' ? 'Available' : humanize(item.failureReason || 'failed')}
              </StatusPill>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</Panel>

