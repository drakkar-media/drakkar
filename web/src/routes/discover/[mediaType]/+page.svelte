<script lang="ts">
  /**
   * Displays the paginated TMDB trending list (movies or TV, per the
   * `mediaType` route param) with a "Load More" button.
   *
   * Reloads from page 1 whenever the route's mediaType changes (see the
   * `routeKey` reactive block below), since this component instance is
   * reused across /discover/movie ↔ /discover/tv navigations.
   */
  import { page } from '$app/state';
  import { afterNavigate } from '$app/navigation';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import { api } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { toastError } from '$lib/toast';
  import type { DiscoverMediaItem, LibraryItem } from '$lib/types';

  let items: DiscoverMediaItem[] = [];
  let currentPage = 1;
  let totalPages = 1;
  let loading = true;
  let loadingMore = false;
  let mediaType: 'movie' | 'tv' = 'movie';
  let routeKey = '';

  /** Adapts a TMDB discover result into the LibraryItem shape PosterCard expects, marked as not-in-library. */
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
      queueState: '',
      failureReason: '',
      tmdbId: item.tmdbId,
      imdbId: item.imdbId
    };
  }

  async function loadInitial() {
    loading = true;
    try {
      mediaType = page.params.mediaType === 'tv' ? 'tv' : 'movie';
      const result = await api.discoverList(mediaType, 1);
      items = result.items;
      currentPage = result.page;
      totalPages = result.totalPages;
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      items = [];
      currentPage = 1;
      totalPages = 1;
    } finally {
      loading = false;
    }
  }

  async function loadMore() {
    if (loadingMore || currentPage >= totalPages) return;
    loadingMore = true;
    try {
      const result = await api.discoverList(mediaType, currentPage + 1);
      // TMDB's trending pages can overlap (an item shifting pages between
      // requests), so de-dupe the appended page against what's already shown.
      const seen = new Set(items.map((item) => `${item.mediaType}:${item.tmdbId ?? item.title}`));
      for (const item of result.items) {
        const key = `${item.mediaType}:${item.tmdbId ?? item.title}`;
        if (!seen.has(key)) {
          seen.add(key);
          items = [...items, item];
        }
      }
      currentPage = result.page;
      totalPages = result.totalPages;
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      loadingMore = false;
    }
  }

  // Re-run the initial load when navigating between mediaType route values,
  // since SvelteKit reuses this component instance rather than remounting it.
  // Reading page.params from a $: block does not reliably re-run on
  // client-side navigation in this app -- same root cause fixed in
  // +layout.svelte, AppShell.svelte, the details page, and the search page.
  // afterNavigate + a plain reassignment, plus an eager synchronous read for
  // the very first load (afterNavigate does not fire for that one), is the
  // reliable alternative used there too.
  routeKey = page.params.mediaType ?? 'movie';
  void loadInitial();
  afterNavigate((nav) => {
    const nextKey = nav.to?.params?.mediaType ?? 'movie';
    if (nextKey !== routeKey) {
      routeKey = nextKey;
      void loadInitial();
    }
  });
</script>

<svelte:head><title>{mediaType === 'movie' ? 'Trending Movies' : 'Trending TV'} — Drakkar</title></svelte:head>

<div class="flex flex-col gap-5">
  <header class="flex flex-wrap items-end justify-between gap-4">
    <div>
      <h1 class="m-0">{mediaType === 'movie' ? 'Trending Movies' : 'Trending TV Shows'}</h1>
      <p class="mt-1.5 text-sm text-muted-foreground">Daily TMDB trending list with paging.</p>
    </div>
    <a class="rounded-full border border-border px-3.5 py-2.5 text-xs font-bold text-muted-foreground no-underline transition-colors hover:bg-muted hover:text-foreground" href="/dashboard">Back To Dashboard</a>
  </header>

  {#if loading}
    <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-7 text-center text-muted-foreground">Loading…</div>
  {:else if items.length === 0}
    <div class="rounded-[20px] border border-white/[0.06] bg-white/[0.02] p-7 text-center text-muted-foreground">No trending media found.</div>
  {:else}
    <div class="grid grid-cols-[repeat(auto-fill,minmax(140px,1fr))] gap-2.5">
      {#each items as item}
        <PosterCard item={asLibraryLike(item)} href={detailsHref(item)} showStatus={false} />
      {/each}
    </div>

    {#if currentPage < totalPages}
      <div class="flex justify-center">
        <button
          class="h-10.5 rounded-2xl border border-border bg-white/[0.05] px-4.5 text-foreground outline-none transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 disabled:opacity-55"
          on:click={() => void loadMore()} disabled={loadingMore}
        >
          {loadingMore ? 'Loading…' : 'Load More'}
        </button>
      </div>
    {/if}
  {/if}
</div>
