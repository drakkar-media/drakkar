<script lang="ts">
  /**
   * Displays metadata search results (movies and TV shows) for the `?q=` query param
   * populated by the top bar's search box.
   *
   * Re-runs the search reactively whenever the `q` param changes, and none of the
   * results are library items yet — they're adapted via asLibraryLike so they can
   * reuse PosterCard.
   */
  import { page } from '$app/state';
  import { afterNavigate } from '$app/navigation';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import { api } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { toastError } from '$lib/toast';
  import type { DiscoverMediaItem, DiscoverSearchResult, LibraryItem } from '$lib/types';

  let loading = true;
  let query = '';
  let activeQuery = '';
  let result: DiscoverSearchResult = { movies: [], tv: [] };
  // Guards against a slower earlier request's response landing after a
  // faster later one's and clobbering the newer results (e.g. typing
  // quickly navigates through several ?q= values in a row).
  let searchToken = 0;

  /** Adapts a discover-search result into the LibraryItem shape PosterCard expects. */
  function asLibraryLike(item: DiscoverMediaItem): LibraryItem {
    return {
      id: 0,
      mediaType: item.mediaType,
      title: item.title,
      year: item.year,
      overview: item.overview,
      posterUrl: item.posterUrl,
      backdropUrl: item.backdropUrl,
      available: false,
      requestedAt: '',
      queueState: 'requested',
      failureReason: '',
      tmdbId: item.tmdbId,
      imdbId: item.imdbId
    };
  }

  /** Fetches results for the current `q` param; stale responses are dropped via searchToken. */
  async function loadSearch() {
    const token = ++searchToken;
    query = page.url.searchParams.get('q')?.trim() ?? '';
    if (!query) {
      result = { movies: [], tv: [] };
      loading = false;
      return;
    }
    loading = true;
    try {
      const data = await api.discoverSearch(query);
      if (token !== searchToken) return;
      result = data;
    } catch (error) {
      if (token !== searchToken) return;
      toastError(error instanceof Error ? error.message : String(error));
      result = { movies: [], tv: [] };
    } finally {
      if (token === searchToken) loading = false;
    }
  }

  // Reading page.url from a $: block does not reliably re-run on client-side
  // navigation in this app (e.g. searching again from the topbar while
  // already on /search updates the URL but this block never re-fires) --
  // same root cause fixed in +layout.svelte, AppShell.svelte, and the details
  // page. afterNavigate + a plain reassignment, plus an eager synchronous
  // read for the very first load (afterNavigate does not fire for that one),
  // is the reliable alternative used there too.
  activeQuery = page.url.searchParams.get('q')?.trim() ?? '';
  void loadSearch();
  afterNavigate((nav) => {
    const nextQuery = nav.to?.url.searchParams.get('q')?.trim() ?? '';
    if (nextQuery !== activeQuery) {
      activeQuery = nextQuery;
      void loadSearch();
    }
  });
</script>

<svelte:head><title>Search — Drakkar</title></svelte:head>

<div class="flex flex-col gap-5.5">
  <header>
    <h1>Search</h1>
    <p class="mt-1.5 text-sm text-muted-foreground">{query ? `Metadata results for "${query}"` : 'Search movies and shows from top bar.'}</p>
  </header>

  {#if loading}
    <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-7 text-center text-muted-foreground">Searching…</div>
  {:else if !query}
    <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-7 text-center text-muted-foreground">Type in top bar. Press Enter.</div>
  {:else}
    <section class="grid gap-3">
      <div class="flex items-center justify-between gap-3"><h2 class="m-0">Movies</h2><span class="text-sm text-muted-foreground">{result.movies.length}</span></div>
      {#if result.movies.length > 0}
        <div class="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-2.5">
          {#each result.movies as item}
            <PosterCard item={asLibraryLike(item)} href={detailsHref(item)} showStatus={false} />
          {/each}
        </div>
      {:else}
        <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-5 text-center text-muted-foreground">No movies found.</div>
      {/if}
    </section>

    <section class="grid gap-3">
      <div class="flex items-center justify-between gap-3"><h2 class="m-0">TV Shows</h2><span class="text-sm text-muted-foreground">{result.tv.length}</span></div>
      {#if result.tv.length > 0}
        <div class="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-2.5">
          {#each result.tv as item}
            <PosterCard item={asLibraryLike(item)} href={detailsHref(item)} showStatus={false} />
          {/each}
        </div>
      {:else}
        <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-5 text-center text-muted-foreground">No TV shows found.</div>
      {/if}
    </section>
  {/if}
</div>
