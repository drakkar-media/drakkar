<script lang="ts">
  import { page } from '$app/state';
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

  $: {
    const nextKey = page.params.mediaType ?? 'movie';
    if (nextKey !== routeKey) {
      routeKey = nextKey;
      void loadInitial();
    }
  }
</script>

<svelte:head><title>{mediaType === 'movie' ? 'Trending Movies' : 'Trending TV'} — Drakkar</title></svelte:head>

<div class="page">
  <header class="head">
    <div>
      <h1>{mediaType === 'movie' ? 'Trending Movies' : 'Trending TV Shows'}</h1>
      <p>Daily TMDB trending list with paging.</p>
    </div>
    <a class="back-link" href="/dashboard">Back To Dashboard</a>
  </header>

  {#if loading}
    <div class="empty">Loading…</div>
  {:else if items.length === 0}
    <div class="empty">No trending media found.</div>
  {:else}
    <div class="poster-grid">
      {#each items as item}
        <PosterCard item={asLibraryLike(item)} href={detailsHref(item)} showStatus={false} />
      {/each}
    </div>

    {#if currentPage < totalPages}
      <div class="more-wrap">
        <button class="more-btn" on:click={() => void loadMore()} disabled={loadingMore}>
          {loadingMore ? 'Loading…' : 'Load More'}
        </button>
      </div>
    {/if}
  {/if}
</div>

<style>
  .page { display: flex; flex-direction: column; gap: 20px; }
  .head {
    display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; flex-wrap: wrap;
  }
  h1 { margin: 0; }
  p { margin: 6px 0 0; color: hsl(var(--muted-foreground)); font-size: 14px; }
  .back-link {
    text-decoration: none;
    padding: 10px 14px;
    border-radius: 999px;
    border: 1px solid hsl(0 0% 100% / 0.1);
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    font-weight: 700;
  }
  .poster-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 10px;
  }
  @media (min-width: 480px)  { .poster-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); } }
  @media (min-width: 700px)  { .poster-grid { grid-template-columns: repeat(4, minmax(0, 1fr)); } }
  @media (min-width: 900px)  { .poster-grid { grid-template-columns: repeat(6, minmax(0, 1fr)); } }
  @media (min-width: 1400px) { .poster-grid { grid-template-columns: repeat(8, minmax(0, 1fr)); } }
  .more-wrap { display: flex; justify-content: center; }
  .more-btn {
    height: 42px; padding: 0 18px; border-radius: 14px;
    border: 1px solid hsl(0 0% 100% / 0.1);
    background: hsl(0 0% 100% / 0.05);
    color: hsl(var(--foreground));
    cursor: pointer;
  }
  .empty {
    padding: 28px; border-radius: 20px; text-align: center;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.02);
    color: hsl(var(--muted-foreground));
  }
</style>
