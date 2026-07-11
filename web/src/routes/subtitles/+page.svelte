<script lang="ts">
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Link from '@lucide/svelte/icons/link';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { runAction } from '$lib/actions';
  import { debounce } from '$lib/debounce';
  import type { SubtitleLibraryRow } from '$lib/types';

  let items: SubtitleLibraryRow[] = [];
  let total = 0;
  let totalPages = 1;
  let page = 1;
  const pageSize = 25;
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
  let search = '';
  let mediaType: 'all' | 'movie' | 'episode' = 'all';
  let missingOnly = false;
  let selected = new Set<number>();

  $: selectedCount = selected.size;
  $: allVisibleSelected = items.length > 0 && items.every((item) => selected.has(item.libraryItemId));

  async function load() {
    loading = true;
    try {
      const result = await api.subtitleLibrary({ page, pageSize, q: search || undefined, mediaType, missingOnly });
      items = (result.items ?? []).map((item) => ({ ...item, languages: item.languages ?? [] }));
      total = result.total;
      totalPages = result.totalPages;
      selected = new Set(Array.from(selected).filter((id) => items.some((item) => item.libraryItemId === id)));
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function applyFilters() {
    page = 1;
    void load();
  }

  function toggleSelected(id: number, checked: boolean) {
    const next = new Set(selected);
    if (checked) next.add(id);
    else next.delete(id);
    selected = next;
  }

  function toggleAllVisible(checked: boolean) {
    const next = new Set(selected);
    for (const item of items) {
      if (checked) next.add(item.libraryItemId);
      else next.delete(item.libraryItemId);
    }
    selected = next;
  }

  function rowLabel(item: SubtitleLibraryRow): string {
    if (item.mediaType === 'movie') return item.title;
    if (item.seasonNumber && item.episodeNumber) {
      return `${item.showTitle || item.title} — S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')} ${item.title}`;
    }
    return item.showTitle ? `${item.showTitle} — ${item.title}` : item.title;
  }

  async function searchOne(id: number) {
    await runAction(() => api.searchSubtitles(id, []), {
      setWorking: (v) => setBusy(`search-${id}`, v),
      successMessage: () => 'Subtitle search queued',
      afterSuccess: load
    });
  }

  async function deleteOne(id: number) {
    if (typeof window !== 'undefined' && !window.confirm('Delete all subtitles for this item?')) return;
    await runAction(() => api.bulkSubtitleAction('delete', [id]), {
      setWorking: (v) => setBusy(`delete-${id}`, v),
      successMessage: () => 'Subtitles deleted',
      afterSuccess: load
    });
  }

  async function bulkAction(action: 'search' | 'delete') {
    if (selectedCount === 0) return;
    const ids = Array.from(selected);
    if (action === 'delete' && typeof window !== 'undefined' && !window.confirm(`Delete all subtitles for ${ids.length} selected item(s)?`)) return;
    await runAction(() => api.bulkSubtitleAction(action, ids), {
      setWorking: (v) => setBusy(`bulk-${action}`, v),
      successMessage: (result) => (action === 'search' ? `Queued search for ${result.count} item(s)` : `Deleted subtitles for ${result.count} item(s)`),
      afterSuccess: async () => {
        selected = new Set();
        await load();
      }
    });
  }

  onMount(() => {
    void load();
    const debouncedLoad = debounce(() => void load(), 500);
    const unsub = subscribeEvents(() => {
      if (!anyBusy()) debouncedLoad();
    });
    return unsub;
  });
</script>

<svelte:head><title>Subtitles — Drakkar</title></svelte:head>

<PageHeader title="Subtitle Manager" subtitle="Search, download, and clean up subtitles across every movie and TV episode.">
  <Button kind="secondary" on:click={load} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
</PageHeader>

<div class="filter-row">
  <input
    class="filter-input"
    type="search"
    placeholder="Search title…"
    bind:value={search}
    on:keydown={(e) => e.key === 'Enter' && applyFilters()}
  />
  <select class="filter-select" bind:value={mediaType} on:change={applyFilters}>
    <option value="all">All media</option>
    <option value="movie">Movies</option>
    <option value="episode">TV episodes</option>
  </select>
  <label class="filter-checkbox">
    <input type="checkbox" bind:checked={missingOnly} on:change={applyFilters} />
    Missing subtitles only
  </label>
  <Button kind="secondary" on:click={applyFilters} disabled={loading}>Apply</Button>
</div>

<Panel title="Library" subtitle="One row per movie or per TV episode.">
  <div slot="actions">
    <StatusPill tone="neutral">{total} item{total === 1 ? '' : 's'}</StatusPill>
  </div>

  {#if items.length === 0 && !loading}
    <div class="empty-state">No items match these filters.</div>
  {:else}
    <div class="toolbar">
      <label class="select-all">
        <input
          type="checkbox"
          checked={allVisibleSelected}
          disabled={items.length === 0 || isBusy('bulk-search') || isBusy('bulk-delete')}
          on:change={(e) => toggleAllVisible((e.currentTarget as HTMLInputElement).checked)}
        />
        <span>Select visible ({items.length})</span>
      </label>
      <div class="toolbar-actions">
        <StatusPill tone="neutral">{selectedCount} selected</StatusPill>
        <Button kind="secondary" on:click={() => bulkAction('search')} disabled={isBusy('bulk-search') || selectedCount === 0}>
          <SearchCheck size={14} />
          Search Selected
        </Button>
        <Button kind="danger" on:click={() => bulkAction('delete')} disabled={isBusy('bulk-delete') || selectedCount === 0}>
          <Trash2 size={14} />
          Delete Selected
        </Button>
      </div>
    </div>

    <div class="row-list">
      {#each items as item (item.libraryItemId)}
        <div class="row-card">
          <label class="row-checkbox">
            <input
              type="checkbox"
              checked={selected.has(item.libraryItemId)}
              disabled={isBusy('bulk-search') || isBusy('bulk-delete')}
              on:change={(e) => toggleSelected(item.libraryItemId, (e.currentTarget as HTMLInputElement).checked)}
            />
          </label>
          <div class="row-main">
            <div class="row-title">{rowLabel(item)}</div>
            <div class="row-sub">
              {#if item.languages.length > 0}
                {item.languages.join(', ')}
              {:else}
                No subtitles
              {/if}
              {#if item.candidateCount > 0}
                · {item.candidateCount} candidate{item.candidateCount === 1 ? '' : 's'}
              {/if}
            </div>
          </div>
          <div class="row-actions">
            <a href={`/details/${item.mediaType === 'movie' ? 'movie' : 'tv'}/${item.libraryItemId}`} class="library-link">
              <Link size={14} />
              Open
            </a>
            <Button kind="secondary" on:click={() => searchOne(item.libraryItemId)} disabled={isBusy(`search-${item.libraryItemId}`)}>
              <SearchCheck size={14} />
              Search
            </Button>
            <Button kind="danger" on:click={() => deleteOne(item.libraryItemId)} disabled={isBusy(`delete-${item.libraryItemId}`) || item.languages.length === 0}>
              <Trash2 size={14} />
              Delete
            </Button>
          </div>
        </div>
      {/each}
    </div>

    <div class="pager">
      <div class="pager-copy">Page {page} of {totalPages}</div>
      <div class="pager-actions">
        <button type="button" on:click={() => { page = Math.max(1, page - 1); void load(); }} disabled={page === 1 || loading}>Prev</button>
        <button type="button" on:click={() => { page = Math.min(totalPages, page + 1); void load(); }} disabled={page === totalPages || loading}>Next</button>
      </div>
    </div>
  {/if}
</Panel>

<style>
  .filter-row {
    display: flex;
    gap: 10px;
    align-items: center;
    margin-bottom: 16px;
    flex-wrap: wrap;
  }

  .filter-input,
  .filter-select {
    height: 40px;
    padding: 0 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 13px;
  }

  .filter-input {
    flex: 1;
    min-width: 200px;
  }

  .filter-checkbox {
    display: flex;
    align-items: center;
    gap: 8px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .filter-checkbox input {
    width: 16px;
    height: 16px;
  }

  .empty-state {
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .toolbar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    padding: 12px 14px;
    margin-bottom: 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 16px;
    background: hsl(0 0% 100% / 0.03);
  }

  .select-all,
  .toolbar-actions {
    display: flex;
    align-items: center;
    gap: 10px;
  }

  .select-all {
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .select-all input {
    width: 16px;
    height: 16px;
  }

  .row-list {
    display: grid;
    gap: 10px;
  }

  .row-card {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 14px 16px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 20px;
    background: hsl(var(--card) / 0.82);
  }

  .row-checkbox {
    flex: 0 0 auto;
  }

  .row-checkbox input {
    width: 16px;
    height: 16px;
  }

  .row-main {
    flex: 1;
    min-width: 0;
  }

  .row-title {
    font-weight: 600;
  }

  .row-sub {
    margin-top: 4px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .row-actions {
    display: flex;
    align-items: center;
    gap: 8px;
    flex: 0 0 auto;
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
    color: hsl(var(--foreground));
    font-size: 13px;
  }

  .pager {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
    margin-top: 14px;
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

  @media (max-width: 700px) {
    .toolbar,
    .row-card,
    .pager {
      flex-direction: column;
      align-items: flex-start;
    }
  }
</style>
