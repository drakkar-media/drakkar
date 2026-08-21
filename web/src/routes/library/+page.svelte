<script lang="ts">
  /**
   * Displays the monitored media library as a filterable, paginated poster grid.
   *
   * Supports searching by title, filtering by kind (movie/TV) and state
   * (available/downloading/missing), and triggering background Seerr sync and
   * pending-search jobs. Filter/page state is mirrored to the URL query string
   * (see `syncUrl`/`onMount`) so links are bookmarkable and shareable. While a
   * background job is in flight, incoming SSE events and the 30s polling
   * refresh are suppressed to avoid clobbering the "working" state; a
   * debounced reload picks up the result once the job's completion event
   * arrives.
   */
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import SearchIcon from '@lucide/svelte/icons/search';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Play from '@lucide/svelte/icons/play';
  import Pagination from '$lib/components/Pagination.svelte';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import MetricCard from '$lib/components/MetricCard.svelte';
  import Button from '$lib/components/Button.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { runAction } from '$lib/actions';
  import { debounce } from '$lib/debounce';
  import type { LibraryItem, LibraryPage, Status } from '$lib/types';

  let items: LibraryItem[] = [];
  let libraryPage: LibraryPage = { items: [], page: 1, pageSize: 40, total: 0, totalPages: 1, totalMonitored: 0, sumAvailable: 0, sumMissing: 0, countActive: 0 };
  let status: Status | null = null;
  let loading = true;
  let working = false;
  let query = '';
  let kind = 'all';
  let stateFilter = 'all';
  let currentPage = 1;
  const pageSize = 40;

  // status only feeds seerrReady/hydraReady below -- integration config that
  // essentially never changes mid-session -- so it's fetched once on mount
  // (see loadStatus/onMount) rather than every time loadLibrary runs, which
  // this function used to also do on every pagination click, filter change,
  // SSE-triggered reload, and the 30s poll.
  async function loadStatus() {
    try {
      status = await api.status();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    }
  }

  async function loadLibrary() {
    loading = true;
    try {
      libraryPage = await api.library({ page: currentPage, pageSize, q: query.trim(), kind, state: stateFilter });
      items = libraryPage.items;
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function syncRequests() {
    await runAction(() => api.syncRequests(), {
      setWorking: (v) => (working = v),
      successMessage: () => 'Sync started in background — results will appear via SSE',
      afterSuccess: loadLibrary
    });
  }

  async function processPending() {
    await runAction(() => api.searchPendingLibrary(), {
      setWorking: (v) => (working = v),
      successMessage: () => 'Search started in background — results will appear via SSE',
      afterSuccess: loadLibrary
    });
  }

  // Sync current state back to the URL for bookmarking / browser history.
  function syncUrl() {
    const url = new URL(page.url);
    if (kind === 'all') url.searchParams.delete('kind');
    else url.searchParams.set('kind', kind);
    if (query.trim()) url.searchParams.set('q', query.trim());
    else url.searchParams.delete('q');
    if (stateFilter === 'all') url.searchParams.delete('state');
    else url.searchParams.set('state', stateFilter);
    if (currentPage <= 1) url.searchParams.delete('page');
    else url.searchParams.set('page', String(currentPage));
    void goto(`${url.pathname}?${url.searchParams.toString()}`, { replaceState: true, noScroll: true, keepFocus: true });
  }

  function updateFilters(next: { kind?: string; q?: string; state?: string }) {
    kind = next.kind ?? kind;
    query = next.q ?? query;
    stateFilter = next.state ?? stateFilter;
    currentPage = 1;
    void loadLibrary();
    syncUrl();
  }

  function changePage(nextPage: number) {
    currentPage = nextPage;
    void loadLibrary();
    syncUrl();
  }

  onMount(() => {
    // Seed state from URL so bookmarked/shared links work.
    kind = page.url.searchParams.get('kind') ?? 'all';
    query = page.url.searchParams.get('q') ?? '';
    stateFilter = page.url.searchParams.get('state') ?? 'all';
    currentPage = Number(page.url.searchParams.get('page') ?? '1') || 1;

    void loadStatus();
    void loadLibrary();

    const debouncedLoadLibrary = debounce(() => void loadLibrary(), 500);
    const unsub = subscribeEvents((event) => {
      if (event?.kind === 'library.search_pending') {
        const e = event as Record<string, unknown>;
        toastSuccess(`Search Pending complete: searched ${e.searched}, selected ${e.selected}`);
      }
      if (event?.kind === 'requests.sync') {
        const e = event as Record<string, unknown>;
        toastSuccess(`Sync complete: seen ${e.seen ?? 0}, created ${e.created ?? 0}`);
      }
      if (!working) debouncedLoadLibrary();
    });
    const t = window.setInterval(() => {
      if (!working && document.visibilityState === 'visible') void loadLibrary();
    }, 30000);
    return () => { window.clearInterval(t); unsub(); };
  });

  $: seerrReady = status?.integrations?.seerr?.configured ?? false;
  $: hydraReady = status?.integrations?.nzbhydra2?.configured ?? false;

  $: totalPages = Math.max(1, libraryPage.totalPages || 1);
  $: pagedItems = items;
  $: rangeStart = libraryPage.total ? (libraryPage.page - 1) * libraryPage.pageSize + 1 : 0;
  $: rangeEnd = Math.min(libraryPage.page * libraryPage.pageSize, libraryPage.total);

  $: totalAvailable = libraryPage.sumAvailable;
  $: totalMissing   = libraryPage.sumMissing;
  $: activeCount    = libraryPage.countActive;
</script>

<svelte:head><title>Library — Drakkar</title></svelte:head>

<div class="flex flex-col gap-5">
  <!-- Page header -->
  <div class="flex flex-wrap items-start justify-between gap-4">
    <div>
      <h1>Library</h1>
      <p class="mt-1.5 text-sm text-muted-foreground">All monitored titles — requested from Seerr, queued, and available.</p>
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <button
        class="grid size-9 place-items-center rounded-md border border-border bg-transparent text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-45"
        on:click={loadLibrary} disabled={loading || working} title="Refresh"
      >
        <RefreshCw size={15} />
      </button>
      <Button kind="secondary" on:click={syncRequests} disabled={loading || working || !seerrReady}
        title={!seerrReady ? 'Seerr not configured' : ''}>
        <SearchCheck size={14} /> Sync Seerr
      </Button>
      <Button kind="secondary" on:click={processPending} disabled={loading || working || !hydraReady}
        title={!hydraReady ? 'NZBHydra2 not configured' : ''}>
        <Play size={14} /> Search Pending
      </Button>
    </div>
  </div>

  <!-- Metric band (clickable filter tiles) -->
  <div class="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
    <button
      class="rounded-lg text-left outline-2 outline-offset-2 outline-transparent transition-colors {stateFilter === 'all' ? 'outline-primary/45' : 'hover:outline-primary/45'}"
      on:click={() => void updateFilters({ state: 'all' })}
    >
      <MetricCard label="Monitored" value={libraryPage.totalMonitored} detail="titles tracked" />
    </button>
    <button
      class="rounded-lg text-left outline-2 outline-offset-2 outline-transparent transition-colors {stateFilter === 'available' ? 'outline-primary/45' : 'hover:outline-primary/45'}"
      on:click={() => void updateFilters({ state: 'available' })}
    >
      <MetricCard label="Available" value={totalAvailable} detail="movies + episodes" accent />
    </button>
    <button
      class="rounded-lg text-left outline-2 outline-offset-2 outline-transparent transition-colors {stateFilter === 'active' ? 'outline-primary/45' : 'hover:outline-primary/45'}"
      on:click={() => void updateFilters({ state: 'active' })}
    >
      <MetricCard label="Downloading" value={activeCount} detail="in queue" />
    </button>
    <button
      class="rounded-lg text-left outline-2 outline-offset-2 outline-transparent transition-colors {stateFilter === 'missing' ? 'outline-primary/45' : 'hover:outline-primary/45'}"
      on:click={() => void updateFilters({ state: 'missing' })}
    >
      <MetricCard label="Missing" value={totalMissing} detail="movies + episodes" />
    </button>
  </div>

  <!-- Status legend — matches reference Library.tsx groupStatus() -->
  <div class="flex flex-wrap gap-3.5 text-xs text-muted-foreground">
    <span class="flex items-center gap-1.5"><span class="size-2.5 shrink-0 rounded-full" style="background: hsl(var(--status-available))"></span>Completed</span>
    <span class="flex items-center gap-1.5"><span class="size-2.5 shrink-0 rounded-full" style="background: hsl(var(--status-downloading))"></span>Downloading</span>
    <span class="flex items-center gap-1.5"><span class="size-2.5 shrink-0 rounded-full" style="background: hsl(var(--status-unreleased))"></span>Queued</span>
    <span class="flex items-center gap-1.5"><span class="size-2.5 shrink-0 rounded-full" style="background: hsl(var(--status-missing))"></span>Missing</span>
  </div>

  <!-- Filter bar -->
  <div class="flex flex-wrap items-center gap-2.5 rounded-xl border border-border bg-card/60 p-2.5">
    <div class="relative flex min-w-40 flex-1 items-center">
      <SearchIcon size={14} class="pointer-events-none absolute left-2.5 text-muted-foreground" />
      <input
        bind:value={query}
        placeholder="Search titles…"
        class="!h-9 pl-8 text-sm"
        on:change={() => void updateFilters({ q: query })}
        on:keydown={(event) => {
          if (event.key === 'Enter') {
            event.preventDefault();
            void updateFilters({ q: query });
          }
        }}
      />
    </div>
    <div class="flex gap-0.5">
      <button
        class="h-9 rounded-md border border-transparent px-3.5 text-sm font-medium transition-colors {kind === 'all' ? 'border-border bg-muted text-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground'}"
        on:click={() => void updateFilters({ kind: 'all' })}
      >All</button>
      <button
        class="h-9 rounded-md border border-transparent px-3.5 text-sm font-medium transition-colors {kind === 'movie' ? 'border-border bg-muted text-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground'}"
        on:click={() => void updateFilters({ kind: 'movie' })}
      >Movies</button>
      <button
        class="h-9 rounded-md border border-transparent px-3.5 text-sm font-medium transition-colors {kind === 'tv' ? 'border-border bg-muted text-foreground' : 'text-muted-foreground hover:bg-muted hover:text-foreground'}"
        on:click={() => void updateFilters({ kind: 'tv' })}
      >TV</button>
    </div>
  </div>

  <!-- Poster grid -->
  {#if pagedItems.length > 0}
    <div class="flex flex-col items-start justify-between gap-3 text-sm text-muted-foreground sm:flex-row sm:items-center">
      <div>Showing {rangeStart}-{rangeEnd} of {libraryPage.total}</div>
      <div class="inline-flex items-center gap-2">
        <Pagination page={currentPage} {totalPages} showFirstLast={false} on:change={(e) => void changePage(e.detail)} />
      </div>
    </div>
    <div class="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-2.5">
      {#each pagedItems as item}
        <PosterCard {item} />
      {/each}
    </div>
  {:else if loading}
    <div class="rounded-xl border border-border bg-white/[0.02] p-8 text-center text-sm text-muted-foreground">Loading library…</div>
  {:else}
    <div class="rounded-xl border border-border bg-white/[0.02] p-8 text-center text-sm text-muted-foreground">No titles match the current filter.</div>
  {/if}
</div>
