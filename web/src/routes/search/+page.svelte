<script lang="ts">
  import { page } from '$app/state';
  import SearchIcon from '@lucide/svelte/icons/search';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import { api } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { toastError } from '$lib/toast';
  import type { DiscoverMediaItem, DiscoverSearchResult, LibraryItem } from '$lib/types';

  let loading = true;
  let query = '';
  let activeQuery = '';
  let result: DiscoverSearchResult = { movies: [], tv: [] };

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

  async function loadSearch() {
    query = page.url.searchParams.get('q')?.trim() ?? '';
    if (!query) {
      result = { movies: [], tv: [] };
      loading = false;
      return;
    }
    loading = true;
    try {
      result = await api.discoverSearch(query);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
      result = { movies: [], tv: [] };
    } finally {
      loading = false;
    }
  }

  $: {
    const nextQuery = page.url.searchParams.get('q')?.trim() ?? '';
    if (nextQuery !== activeQuery) {
      activeQuery = nextQuery;
      void loadSearch();
    }
  }
</script>

<svelte:head><title>Search — Drakkar</title></svelte:head>

<div class="page">
  <header class="head">
    <div class="title-wrap">
      <div class="icon"><SearchIcon size={18} /></div>
      <div>
        <h1>Search</h1>
        <p>{query ? `Metadata results for "${query}"` : 'Search movies and shows from top bar.'}</p>
      </div>
    </div>
  </header>

  {#if loading}
    <div class="empty">Searching…</div>
  {:else if !query}
    <div class="empty">Type in top bar. Press Enter.</div>
  {:else}
    <section class="section">
      <div class="section-head"><h2>Movies</h2><span>{result.movies.length}</span></div>
      {#if result.movies.length > 0}
        <div class="poster-grid">
          {#each result.movies as item}
            <PosterCard item={asLibraryLike(item)} href={detailsHref(item)} showStatus={false} />
          {/each}
        </div>
      {:else}
        <div class="empty small">No movies found.</div>
      {/if}
    </section>

    <section class="section">
      <div class="section-head"><h2>TV Shows</h2><span>{result.tv.length}</span></div>
      {#if result.tv.length > 0}
        <div class="poster-grid">
          {#each result.tv as item}
            <PosterCard item={asLibraryLike(item)} href={detailsHref(item)} showStatus={false} />
          {/each}
        </div>
      {:else}
        <div class="empty small">No TV shows found.</div>
      {/if}
    </section>
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 22px; }
  .head { display: flex; align-items: flex-end; gap: 16px; }
  .title-wrap { display: flex; align-items: center; gap: 14px; }
  .icon {
    display: grid; place-items: center;
    width: 42px; height: 42px;
    border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
  }
  h1, h2 { margin: 0; }
  p { margin: 6px 0 0; color: hsl(var(--muted-foreground)); font-size: 14px; }
  .section { display: grid; gap: 12px; }
  .section-head {
    display: flex; align-items: center; justify-content: space-between; gap: 12px;
  }
  .section-head span { color: hsl(var(--muted-foreground)); font-size: 13px; }
  .poster-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }
  @media (min-width: 480px)  { .poster-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
  @media (min-width: 700px)  { .poster-grid { grid-template-columns: repeat(4, minmax(0, 1fr)); } }
  @media (min-width: 900px)  { .poster-grid { grid-template-columns: repeat(5, minmax(0, 1fr)); } }
  @media (min-width: 1100px) { .poster-grid { grid-template-columns: repeat(6, minmax(0, 1fr)); } }
  @media (min-width: 1400px) { .poster-grid { grid-template-columns: repeat(8, minmax(0, 1fr)); } }
  .empty {
    padding: 28px;
    border-radius: 20px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.02);
    color: hsl(var(--muted-foreground));
    text-align: center;
  }
  .empty.small { padding: 20px; }
</style>
