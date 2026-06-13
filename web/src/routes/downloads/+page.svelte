<script lang="ts">
  import { onMount } from 'svelte';
  import LinkIcon from '@lucide/svelte/icons/link';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import RotateCcw from '@lucide/svelte/icons/rotate-ccw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Upload from '@lucide/svelte/icons/upload';
  import Link from '@lucide/svelte/icons/link';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import type { QueueItem } from '$lib/types';

  let uploading = false;
  let nzbUrl = '';
  let addingUrl = false;

  let items: QueueItem[] = [];
  let loading = true;
  let working = false;
  let tab: 'queue' | 'history' = 'queue';
  let queuePage = 1;
  let historyPage = 1;

  const activeStates = ['requested', 'searching', 'ranking', 'selected', 'fetching_nzb', 'indexing', 'preflight', 'publishing'];
  const doneStates = ['available', 'failed'];
  const queuePageSize = 8;
  const historyPageSize = 12;

  $: queueItems = items.filter((item) => activeStates.includes(item.state));
  $: historyItems = items.filter((item) => doneStates.includes(item.state));
  $: failedItems = items.filter((item) => item.state === 'failed');
  $: totalSegments = queueItems.reduce((sum, item) => sum + (item.nzbSegmentCount || 0), 0);
  $: queueTotalPages = Math.max(1, Math.ceil(queueItems.length / queuePageSize));
  $: historyTotalPages = Math.max(1, Math.ceil(historyItems.length / historyPageSize));
  $: if (queuePage > queueTotalPages) queuePage = queueTotalPages;
  $: if (historyPage > historyTotalPages) historyPage = historyTotalPages;
  $: pagedQueueItems = queueItems.slice((queuePage - 1) * queuePageSize, queuePage * queuePageSize);
  $: pagedHistoryItems = historyItems.slice((historyPage - 1) * historyPageSize, historyPage * historyPageSize);
  $: queueRangeStart = queueItems.length ? (queuePage - 1) * queuePageSize + 1 : 0;
  $: queueRangeEnd = Math.min(queuePage * queuePageSize, queueItems.length);
  $: historyRangeStart = historyItems.length ? (historyPage - 1) * historyPageSize + 1 : 0;
  $: historyRangeEnd = Math.min(historyPage * historyPageSize, historyItems.length);

  async function load() {
    loading = true;
    try {
      const queue = await api.queue();
      items = queue.items ?? [];
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function refreshItems() {
    try {
      const queue = await api.queue();
      const fresh = queue.items ?? [];
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

  async function retryItem(id: number) {
    working = true;
    try {
      await api.retryQueue(id);
      toastSuccess('Retry queued');
      await load();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      working = false;
    }
  }

  async function retryAll() {
    working = true;
    try {
      const result = await api.retryFailedQueue();
      toastSuccess(`Retried ${result.retried}`);
      await load();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      working = false;
    }
  }

  async function clearFailed() {
    working = true;
    try {
      const result = await api.clearFailedQueue();
      toastSuccess(`Cleared ${result.cleared} failed item${result.cleared === 1 ? '' : 's'}`);
      await load();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      working = false;
    }
  }

  async function processPending() {
    working = true;
    try {
      const result = await api.searchPendingLibrary();
      toastSuccess(`Processed ${result.processed}, selected ${result.selected}`);
      await load();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      working = false;
    }
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

  onMount(() => {
    void load();
    const unsub = subscribeEvents(() => {
      if (!working) void refreshItems();
    });
    const timer = window.setInterval(() => {
      if (!working) void refreshItems();
    }, 15000);
    return () => {
      window.clearInterval(timer);
      unsub();
    };
  });
</script>

<svelte:head><title>Downloads — Drakkar</title></svelte:head>

<PageHeader title="Downloads" subtitle="Queue, history, and active NZB processing in one place.">
  <Button kind="secondary" on:click={load} disabled={loading || working}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <Button kind="secondary" on:click={processPending} disabled={working}>
    <SearchCheck size={14} />
    Process Pending
  </Button>
  {#if failedItems.length > 0}
    <Button kind="secondary" on:click={retryAll} disabled={working}>
      <RotateCcw size={14} />
      Retry Failed ({failedItems.length})
    </Button>
    <Button kind="danger" on:click={clearFailed} disabled={working}>
      <Trash2 size={14} />
      Clear Failed
    </Button>
  {/if}
  <label class="upload-btn" class:disabled={uploading} title="Upload NZB file">
    <Upload size={14} />
    {uploading ? 'Uploading…' : 'Upload NZB'}
    <input
      type="file"
      accept=".nzb,application/x-nzb,application/xml,text/xml"
      class="upload-input"
      disabled={uploading}
      on:change={async (e) => {
        const file = (e.currentTarget as HTMLInputElement).files?.[0];
        if (!file) return;
        uploading = true;
        try {
          await api.addNzb(file);
          toastSuccess(`${file.name} queued`);
          await load();
        } catch (err) {
          toastError(err instanceof Error ? err.message : String(err));
        } finally {
          uploading = false;
          (e.currentTarget as HTMLInputElement).value = '';
        }
      }}
    />
  </label>
</PageHeader>

<section class="summary-grid">
  <div class="summary-card">
    <div class="summary-value">{queueItems.length}</div>
    <div class="summary-label">Active queue jobs</div>
  </div>
  <div class="summary-card">
    <div class="summary-value">{historyItems.length}</div>
    <div class="summary-label">History rows</div>
  </div>
  <div class="summary-card">
    <div class="summary-value">{failedItems.length}</div>
    <div class="summary-label">Failed items</div>
  </div>
  <div class="summary-card">
    <div class="summary-value">{totalSegments}</div>
    <div class="summary-label">Segments in flight</div>
  </div>
</section>

<form class="url-row" on:submit|preventDefault={async () => {
  const url = nzbUrl.trim();
  if (!url) return;
  addingUrl = true;
  try {
    await api.addNzbUrl(url);
    toastSuccess('NZB queued from URL');
    nzbUrl = '';
    await load();
  } catch (err) {
    toastError(err instanceof Error ? err.message : String(err));
  } finally {
    addingUrl = false;
  }
}}>
  <div class="url-input-wrap">
    <Link size={14} />
    <input bind:value={nzbUrl} type="url" placeholder="Paste NZB URL to import…" class="url-input" disabled={addingUrl} />
  </div>
  <Button kind="secondary" disabled={!nzbUrl.trim() || addingUrl}>
    {addingUrl ? 'Adding…' : 'Add NZB URL'}
  </Button>
</form>

<div class="tab-row">
  <button class:active={tab === 'queue'} on:click={() => (tab = 'queue')}>queue</button>
  <button class:active={tab === 'history'} on:click={() => (tab = 'history')}>history</button>
</div>

<Panel
  title={tab === 'queue' ? 'Queue' : 'History'}
  subtitle={tab === 'queue' ? 'Active lifecycle rows from request to publication.' : 'Completed and failed rows.'}
>
  <div slot="actions">
    <StatusPill tone="neutral">{tab === 'queue' ? `${queueItems.length} active` : `${historyItems.length} rows`}</StatusPill>
  </div>

  {#if tab === 'queue'}
    {#if queueItems.length === 0 && !loading}
      <div class="empty-state">Queue empty. Nothing currently processing.</div>
    {:else}
      <div class="pager">
        <div class="pager-copy">Showing {queueRangeStart}-{queueRangeEnd} of {queueItems.length}</div>
        <div class="pager-actions">
          <button on:click={() => (queuePage = Math.max(1, queuePage - 1))} disabled={queuePage === 1}>Prev</button>
          <span>{queuePage}/{queueTotalPages}</span>
          <button on:click={() => (queuePage = Math.min(queueTotalPages, queuePage + 1))} disabled={queuePage === queueTotalPages}>Next</button>
        </div>
      </div>
      <div class="row-list">
        {#each pagedQueueItems as item (item.queueItemId)}
          {@const pct = stageProgress(item.state)}
          <div class="row-card">
            <div class="row-head">
              <div>
                <div class="row-title">{item.libraryTitle}</div>
                <div class="row-sub">
                  {item.nzbFileName ? `${item.nzbFileName} · ` : ''}{item.nzbSegmentCount} segments
                </div>
              </div>
              <Button kind="secondary" on:click={() => retryItem(item.queueItemId)} disabled={working}>
                <RotateCcw size={14} />
                Retry
              </Button>
            </div>
            <div class="progress-track"><div class="progress-fill" style={`width:${pct}%`}></div></div>
            <div class="row-foot">
              <span>{stageLabel(item)}</span>
              <span class="mono">{pct}%</span>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {:else}
    {#if historyItems.length === 0 && !loading}
      <div class="empty-state">No history yet.</div>
    {:else}
      <div class="pager">
        <div class="pager-copy">Showing {historyRangeStart}-{historyRangeEnd} of {historyItems.length}</div>
        <div class="pager-actions">
          <button on:click={() => (historyPage = Math.max(1, historyPage - 1))} disabled={historyPage === 1}>Prev</button>
          <span>{historyPage}/{historyTotalPages}</span>
          <button on:click={() => (historyPage = Math.min(historyTotalPages, historyPage + 1))} disabled={historyPage === historyTotalPages}>Next</button>
        </div>
      </div>
      <div class="row-list">
        {#each pagedHistoryItems as item (item.queueItemId)}
          <div class={`row-card ${item.state === 'failed' ? 'failed' : ''}`}>
            <div class="row-head">
              <div>
                <div class="row-title">{item.libraryTitle}</div>
                <div class="row-sub">
                  {item.nzbFileName ? `${item.nzbFileName} · ` : ''}{item.nzbSegmentCount} segments
                </div>
              </div>
              <div class="history-actions">
                {#if item.state === 'available' && item.libraryItemId}
                  <a href={`/library/${item.libraryItemId}`} class="library-link">
                    <LinkIcon size={14} />
                    Open
                  </a>
                {/if}
                {#if item.state === 'failed'}
                  <Button kind="secondary" on:click={() => retryItem(item.queueItemId)} disabled={working}>
                    <RotateCcw size={14} />
                    Retry
                  </Button>
                {/if}
              </div>
            </div>
            <div class="row-foot">
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

<style>
  .summary-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 14px;
    margin-bottom: 20px;
  }

  .summary-card,
  .row-card {
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 20px;
    background: hsl(var(--card) / 0.82);
  }

  .summary-card {
    padding: 18px 20px;
  }

  .summary-value {
    font-size: 2rem;
    font-weight: 700;
    line-height: 1;
  }

  .summary-label,
  .row-sub,
  .empty-state {
    margin-top: 8px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .tab-row {
    display: flex;
    gap: 6px;
    margin: 20px 0 12px;
  }

  .tab-row button {
    min-height: 36px;
    padding: 0 14px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: transparent;
    color: hsl(var(--muted-foreground));
    cursor: pointer;
    text-transform: lowercase;
    font-weight: 700;
  }

  .tab-row button.active {
    background: hsl(var(--primary));
    border-color: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
  }

  .row-list {
    display: grid;
    gap: 10px;
  }

  .pager {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-bottom: 14px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .pager-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }

  .pager-actions button {
    min-height: 32px;
    padding: 0 10px;
    border-radius: 10px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.03);
    color: hsl(var(--foreground));
    cursor: pointer;
  }

  .pager-actions button:disabled {
    opacity: 0.45;
    cursor: default;
  }

  .row-card {
    padding: 16px 18px;
  }

  .row-card.failed {
    border-color: hsl(var(--danger) / 0.28);
    background: hsl(var(--danger) / 0.05);
  }

  .row-head,
  .row-foot,
  .history-actions {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .row-title {
    font-weight: 600;
  }

  .progress-track {
    height: 8px;
    border-radius: 999px;
    background: hsl(0 0% 100% / 0.06);
    overflow: hidden;
    margin: 14px 0 10px;
  }

  .progress-fill {
    height: 100%;
    border-radius: 999px;
    background: hsl(var(--primary));
    transition: width 0.4s ease;
  }

  .row-foot {
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .library-link {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 36px;
    padding: 0 12px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
  }

  .url-row {
    display: flex;
    gap: 10px;
    align-items: center;
    margin-bottom: 16px;
  }

  .url-input-wrap {
    flex: 1;
    display: flex;
    align-items: center;
    gap: 10px;
    height: 40px;
    padding: 0 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--muted-foreground));
  }

  .url-input {
    flex: 1;
    background: transparent;
    border: none;
    outline: none;
    color: hsl(var(--foreground));
    font-size: 13px;
  }

  .url-input::placeholder { color: hsl(var(--muted-foreground)); }
  .url-input:disabled { opacity: 0.6; }

  .upload-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 36px;
    padding: 0 12px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 13px;
    font-weight: 600;
    cursor: pointer;
    transition: background .12s;
  }

  .upload-btn:hover:not(.disabled) { background: hsl(0 0% 100% / 0.08); }
  .upload-btn.disabled { opacity: 0.55; cursor: default; }
  .upload-input { display: none; }

  @media (max-width: 900px) {
    .summary-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }

  @media (max-width: 700px) {
    .pager,
    .row-head,
    .row-foot {
      flex-direction: column;
      align-items: flex-start;
    }
  }
</style>
