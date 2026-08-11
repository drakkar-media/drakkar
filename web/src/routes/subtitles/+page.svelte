<script lang="ts">
  /**
   * Displays a paginated, filterable library of movies/episodes with their
   * subtitle coverage, letting operators search or delete subtitles per-item
   * or in bulk for the current selection.
   *
   * Any SSE event triggers a debounced reload (skipped while a search/delete
   * is in flight). Selection is tracked by library item id and is pruned
   * against whatever the current page returns, so it does not survive
   * navigating to a different page.
   */
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import Link from '@lucide/svelte/icons/link';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import * as Select from '$lib/components/ui/select/index.js';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { runAction, confirmed } from '$lib/actions';
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

  $: mediaTypeLabel = mediaType === 'movie' ? 'Movies' : mediaType === 'episode' ? 'TV episodes' : 'All media';
  $: selectedCount = selected.size;
  $: allVisibleSelected = items.length > 0 && items.every((item) => selected.has(item.libraryItemId));

  /** Loads the current page/filter set and drops any selected ids that fell off the new page. */
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

  /** Formats a row's display title, including season/episode numbers when the item is a TV episode. */
  function rowLabel(item: SubtitleLibraryRow): string {
    if (item.mediaType === 'movie') return item.title;
    if (item.seasonNumber && item.episodeNumber) {
      return `${item.showTitle || item.title} — S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')}`;
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
    if (!confirmed('Delete all subtitles for this item?')) return;
    await runAction(() => api.bulkSubtitleAction('delete', [id]), {
      setWorking: (v) => setBusy(`delete-${id}`, v),
      successMessage: () => 'Subtitle deletion queued',
      afterSuccess: load
    });
  }

  /** Runs search or delete for the whole current selection, then clears it on success. */
  async function bulkAction(action: 'search' | 'delete') {
    if (selectedCount === 0) return;
    const ids = Array.from(selected);
    if (action === 'delete' && !confirmed(`Delete all subtitles for ${ids.length} selected item(s)?`)) return;
    await runAction(() => api.bulkSubtitleAction(action, ids), {
      setWorking: (v) => setBusy(`bulk-${action}`, v),
      successMessage: (result) => (action === 'search' ? `Queued search for ${result.count} item(s)` : `Queued deletion for ${result.count} item(s)`),
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

<div class="mb-4 flex flex-wrap items-center gap-2.5">
  <input
    class="min-w-50 flex-1"
    type="search"
    placeholder="Search title…"
    bind:value={search}
    on:keydown={(e) => e.key === 'Enter' && applyFilters()}
  />
  <Select.Root type="single" bind:value={mediaType} onValueChange={applyFilters}>
    <Select.Trigger class="w-40">{mediaTypeLabel}</Select.Trigger>
    <Select.Content>
      <Select.Item value="all">All media</Select.Item>
      <Select.Item value="movie">Movies</Select.Item>
      <Select.Item value="episode">TV episodes</Select.Item>
    </Select.Content>
  </Select.Root>
  <label class="flex items-center gap-2 text-sm text-muted-foreground">
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
    <div class="text-sm text-muted-foreground">No items match these filters.</div>
  {:else}
    <div class="mb-3.5 flex flex-wrap items-center justify-between gap-3 rounded-2xl border border-border bg-white/[0.03] px-3.5 py-3">
      <label class="flex items-center gap-2.5 text-sm text-muted-foreground">
        <input
          type="checkbox"
          checked={allVisibleSelected}
          disabled={items.length === 0 || isBusy('bulk-search') || isBusy('bulk-delete')}
          on:change={(e) => toggleAllVisible((e.currentTarget as HTMLInputElement).checked)}
        />
        <span>Select visible ({items.length})</span>
      </label>
      <div class="flex items-center gap-2.5">
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

    <div class="grid gap-2.5">
      {#each items as item (item.libraryItemId)}
        <div class="flex flex-wrap items-center gap-3.5 rounded-xl border border-border bg-card/80 px-4 py-3.5 max-sm:flex-col max-sm:items-start">
          <label class="shrink-0">
            <input
              type="checkbox"
              checked={selected.has(item.libraryItemId)}
              disabled={isBusy('bulk-search') || isBusy('bulk-delete')}
              on:change={(e) => toggleSelected(item.libraryItemId, (e.currentTarget as HTMLInputElement).checked)}
            />
          </label>
          <div class="min-w-0 flex-1">
            <div class="font-semibold">{rowLabel(item)}</div>
            <div class="mt-1 text-sm text-muted-foreground">
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
          <div class="flex shrink-0 items-center gap-2">
            <a href={`/details/${item.mediaType === 'movie' ? 'movie' : 'tv'}/${item.libraryItemId}`} class="inline-flex min-h-9 items-center gap-1.5 rounded-md border border-border bg-muted/40 px-3 text-sm">
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

    <div class="mt-3.5 flex items-center justify-between gap-3 text-sm text-muted-foreground max-sm:flex-col max-sm:items-start">
      <div>Page {page} of {totalPages}</div>
      <div class="inline-flex items-center gap-2">
        <Button kind="secondary" on:click={() => { page = Math.max(1, page - 1); void load(); }} disabled={page === 1 || loading}>Prev</Button>
        <Button kind="secondary" on:click={() => { page = Math.min(totalPages, page + 1); void load(); }} disabled={page === totalPages || loading}>Next</Button>
      </div>
    </div>
  {/if}
</Panel>
