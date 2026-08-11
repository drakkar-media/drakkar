<script lang="ts">
  /**
   * Displays the home dashboard: an auto-advancing hero carousel of trending
   * movies/TV, system status tiles (cache sizes, FUSE mount) and integration
   * health chips, and media rows for recently-added and trending content.
   *
   * Refreshes on SSE events (debounced) and falls back to a 2-minute
   * visibility-gated poll if an event is missed.
   */
  import { onMount } from 'svelte';
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import Activity from '@lucide/svelte/icons/activity';
  import Database from '@lucide/svelte/icons/database';
  import Layers from '@lucide/svelte/icons/layers';
  import HardDrive from '@lucide/svelte/icons/hard-drive';
  import CheckCircle from '@lucide/svelte/icons/check-circle';
  import AlertCircle from '@lucide/svelte/icons/alert-circle';
  import MediaRow from '$lib/components/MediaRow.svelte';
  import { Badge } from '$lib/components/ui/badge/index.js';
  import { Button } from '$lib/components/ui/button/index.js';
  import { api, subscribeEvents } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { toastError, toastSuccess } from '$lib/toast';
  import { bytes as fmt } from '$lib/format';
  import { debounce } from '$lib/debounce';
  import type { DashboardHome, LibraryItem, PrivacyStatus, Status } from '$lib/types';

  let home: DashboardHome | null = null;
  let status: Status | null = null;
  let privacyStatus: PrivacyStatus | null = null;
  let loading = true;
  let heroIndex = 0;
  let heroTimer: number;
  let heroCarouselKey = '';

  async function loadAll() {
    loading = true;
    try {
      const [dashboard, appStatus] = await Promise.all([api.dashboardHome(), api.status()]);
      home = dashboard;
      status = appStatus;
    }
    catch (err) { toastError(err instanceof Error ? err.message : String(err)); }
    finally { loading = false; }
    try {
      privacyStatus = await api.getPrivacyStatus();
    } catch {
      // Non-fatal -- the privacy chip just doesn't render until reachable.
    }
  }

  async function requestItem(item: LibraryItem) {
    if (!item.tmdbId) return;
    const mediaType = (item.mediaType === 'tv' || item.mediaType === 'episode') ? 'tv' : 'movie';
    try {
      const result = await api.requestMedia(item.tmdbId, mediaType);
      toastSuccess(result.queued ? 'Requested — finishing up in background' : `Requested — ${result.created} item(s) added`);
      void loadAll();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    }
  }

  function startCarousel(items: LibraryItem[]) {
    // Skip the restart when the trending set is unchanged — loadAll() reruns
    // on every SSE message and 120s poll tick, and without this guard each
    // rerun cleared+restarted the timer, resetting the 7s countdown so the
    // carousel could go long stretches without ever auto-advancing.
    const key = items.map((item) => item.id).join(',');
    if (key === heroCarouselKey) return;
    heroCarouselKey = key;
    heroIndex = 0;
    clearInterval(heroTimer);
    if (items.length > 1) {
      heroTimer = window.setInterval(() => {
        heroIndex = (heroIndex + 1) % items.length;
      }, 7000);
    }
  }

  const debouncedLoadAll = debounce(() => void loadAll(), 500);

  onMount(() => {
    void loadAll();
    const unsub = subscribeEvents(() => debouncedLoadAll());
    // Falls back to polling every 2 minutes (only while the tab is visible)
    // in case an SSE event is missed or the connection drops.
    const t = window.setInterval(() => {
      if (document.visibilityState === 'visible') void loadAll();
    }, 120000);
    return () => { window.clearInterval(t); window.clearInterval(heroTimer); unsub(); };
  });

  $: heroItems = [...(home?.trendingMovies ?? []), ...(home?.trendingTv ?? [])].slice(0, 10);
  // (Re)starts the hero auto-advance timer whenever the trending set changes.
  $: { if (heroItems.length) startCarousel(heroItems); }
  $: hero = heroItems[heroIndex] ?? heroItems[0];
  $: integrations = status?.integrations;
  $: heroKind = hero?.mediaType === 'episode' ? 'tv' : hero?.mediaType;
  $: heroStatus = hero?.available ? 'available' : hero?.queueState || null;
  $: heroInLibrary = !!(hero?.id);
</script>

<svelte:head><title>Dashboard — Drakkar</title></svelte:head>

<!-- Hero carousel -->
{#if hero}
  <section class="relative mb-7 h-[420px] overflow-hidden rounded-2xl border border-white/[0.07] md:h-[500px]">
    {#if hero.backdropUrl}
      <img class="absolute inset-0 size-full object-cover" src={hero.backdropUrl} alt="" />
    {/if}
    <div
      class="absolute inset-0"
      style="background: linear-gradient(90deg, hsl(215 36% 4% / 0.95) 0%, hsl(215 36% 4% / 0.65) 50%, transparent 100%), linear-gradient(0deg, hsl(215 36% 4% / 0.6) 0%, transparent 50%);"
    ></div>
    <div class="relative z-10 flex h-full max-w-[680px] flex-col justify-end p-4 sm:p-8 sm:pl-[72px]">
      <div class="mb-3 flex gap-2">
        <Badge variant="outline" class="border-white/15 bg-white/8 uppercase tracking-wide">{heroKind}</Badge>
        {#if hero.year}<Badge variant="outline" class="border-white/15 bg-white/8 uppercase tracking-wide">{hero.year}</Badge>{/if}
        {#if heroStatus}<Badge variant="outline" class="border-white/15 bg-white/8 uppercase tracking-wide">{heroStatus}</Badge>{/if}
        {#if hero.availableCount || hero.missingCount}
          <Badge variant="outline" class="border-white/15 bg-white/8 uppercase tracking-wide">{hero.availableCount ?? 0} avail / {hero.missingCount ?? 0} miss</Badge>
        {/if}
      </div>
      <h1 class="mb-2.5 text-[clamp(1.8rem,4vw,3rem)] font-bold leading-[1.05]">{hero.title}</h1>
      {#if hero.overview}
        <p class="mb-5 line-clamp-3 max-w-[560px] text-sm leading-relaxed text-foreground/80">{hero.overview}</p>
      {/if}
      <div class="mt-0.5 flex flex-wrap gap-2.5">
        <Button href={detailsHref(hero)} size="lg" class="h-[42px] rounded-xl px-5 text-[13px] font-bold">More Info</Button>
        {#if heroInLibrary}
          <Button href={`/library/${hero.id}`} variant="outline" size="lg" class="h-[42px] rounded-xl border-white/10 bg-white/8 px-5 text-[13px] font-bold">Open Library</Button>
        {:else if hero.tmdbId}
          <Button onclick={() => hero && requestItem(hero)} variant="outline" size="lg" class="h-[42px] rounded-xl border-white/10 bg-white/8 px-5 text-[13px] font-bold">+ Request</Button>
        {/if}
      </div>
    </div>
    {#if heroItems.length > 1}
      <button
        class="absolute left-3.5 top-1/2 z-20 hidden size-10 -translate-y-1/2 place-items-center rounded-full border border-white/15 bg-black/40 text-foreground sm:grid"
        aria-label="Previous hero"
        on:click={() => { heroIndex = (heroIndex - 1 + heroItems.length) % heroItems.length; }}
      >
        <ChevronLeft size={18} />
      </button>
      <button
        class="absolute right-3.5 top-1/2 z-20 hidden size-10 -translate-y-1/2 place-items-center rounded-full border border-white/15 bg-black/40 text-foreground sm:grid"
        aria-label="Next hero"
        on:click={() => { heroIndex = (heroIndex + 1) % heroItems.length; }}
      >
        <ChevronRight size={18} />
      </button>
      <div class="absolute bottom-4 left-1/2 z-20 flex -translate-x-1/2 gap-1.5 rounded-full border border-white/10 bg-black/35 px-3 py-2">
        {#each heroItems as _, i}
          <button
            aria-label={`Hero ${i + 1}`}
            class="h-1 rounded-full bg-white/30 transition-all {i === heroIndex ? 'w-7 bg-primary' : 'w-3.5'}"
            on:click={() => (heroIndex = i)}
          ></button>
        {/each}
      </div>
    {/if}
  </section>
{:else if loading}
  <div class="mb-7 grid h-[200px] place-items-center text-sm text-muted-foreground">Loading dashboard…</div>
{/if}

<div class="flex flex-col gap-6">
  <!-- System status tiles -->
  {#if status}
    <div class="grid grid-cols-2 gap-2.5 sm:grid-cols-4">
      <div class="flex items-center gap-3 rounded-xl border border-white/[0.07] bg-card/80 p-3.5 text-primary">
        <HardDrive size={14} />
        <div>
          <div class="mb-0.5 text-[11px] font-semibold text-muted-foreground">Disk Cache</div>
          <div class="text-[13px] font-bold text-foreground">{fmt(status.diskCacheLimitBytes)} limit</div>
        </div>
      </div>
      <div class="flex items-center gap-3 rounded-xl border border-white/[0.07] bg-card/80 p-3.5 text-primary">
        <Layers size={14} />
        <div>
          <div class="mb-0.5 text-[11px] font-semibold text-muted-foreground">Read-ahead</div>
          <div class="text-[13px] font-bold text-foreground">{fmt(status.readAheadLimitBytes)}</div>
        </div>
      </div>
      <div class="flex items-center gap-3 rounded-xl border border-white/[0.07] bg-card/80 p-3.5 text-primary">
        <Database size={14} />
        <div>
          <div class="mb-0.5 text-[11px] font-semibold text-muted-foreground">Hot Cache</div>
          <div class="text-[13px] font-bold text-foreground">{fmt(status.memoryHotCacheBytes)}</div>
        </div>
      </div>
      <div class="flex items-center gap-3 rounded-xl border border-white/[0.07] bg-card/80 p-3.5 text-primary">
        <Activity size={14} />
        <div>
          <div class="mb-0.5 text-[11px] font-semibold text-muted-foreground">FUSE Mount</div>
          <div class="max-w-[120px] truncate text-[11px] font-bold text-foreground">{status.fuseMountPath || '—'}</div>
        </div>
      </div>
    </div>

    <!-- Integration health -->
    {#if integrations}
      <div class="flex flex-wrap gap-2">
        {#each Object.entries(integrations) as [name, info]}
          {#if name !== 'subtitleProviders' && typeof info === 'object' && info !== null && !Array.isArray(info) && 'enabled' in info}
            <div class="flex items-center gap-1.5 rounded-xl border border-white/[0.07] bg-card/70 px-3 py-1.5 text-xs font-semibold {!info.enabled ? 'opacity-50' : ''}">
              <svelte:component
                this={info.enabled && info.configured ? CheckCircle : AlertCircle}
                size={12}
                style={info.enabled && info.configured ? 'color: hsl(var(--status-available))' : 'color: hsl(var(--status-warning))'}
              />
              <span>{name}</span>
              <span class="text-[11px] {info.enabled && info.configured ? '' : 'text-muted-foreground'}" style={info.enabled && info.configured ? 'color: hsl(var(--status-available))' : ''}>
                {!info.enabled ? 'off' : info.configured ? 'ok' : 'not configured'}
              </span>
            </div>
          {/if}
        {/each}
        {#if privacyStatus}
          {@const privacyOk = privacyStatus.mode === 'direct' || privacyStatus.status === 'connected'}
          <div class="flex items-center gap-1.5 rounded-xl border border-white/[0.07] bg-card/70 px-3 py-1.5 text-xs font-semibold {privacyStatus.status === 'error' ? 'opacity-50' : ''}">
            <svelte:component
              this={privacyOk ? CheckCircle : AlertCircle}
              size={12}
              style={privacyOk ? 'color: hsl(var(--status-available))' : 'color: hsl(var(--status-warning))'}
            />
            <span>privacy</span>
            <span class="text-[11px] {privacyOk ? '' : 'text-muted-foreground'}" style={privacyOk ? 'color: hsl(var(--status-available))' : ''}>
              {privacyStatus.mode}{privacyStatus.mode !== 'direct' ? ` · ${privacyStatus.status}` : ''}
            </span>
          </div>
        {/if}
      </div>
    {/if}
  {/if}

  {#if (home?.recentlyAdded ?? []).length > 0}
    <MediaRow
      title="Recently Added"
      subtitle="Latest completed media in your library."
      items={home?.recentlyAdded ?? []}
      href="/library"
      linkLabel="View Library →"
      itemWidth={140}
    />
  {/if}

  {#if (home?.trendingMovies ?? []).length > 0}
    <MediaRow
      title="Trending Movies"
      subtitle="Popular movies from TMDB."
      items={home?.trendingMovies ?? []}
      href="/discover/movie"
      linkLabel="Browse →"
      itemWidth={140}
      onRequest={requestItem}
    />
  {/if}

  {#if (home?.trendingTv ?? []).length > 0}
    <MediaRow
      title="Trending TV Shows"
      subtitle="Popular TV shows from TMDB."
      items={home?.trendingTv ?? []}
      href="/discover/tv"
      linkLabel="Browse →"
      itemWidth={140}
      onRequest={requestItem}
    />
  {/if}

  {#if !loading && !home?.recentlyAdded?.length && !(home?.trendingMovies ?? []).length}
    <div class="rounded-2xl border border-white/[0.06] bg-white/[0.02] p-8 text-center text-sm text-muted-foreground">
      No media yet. Sync Seerr requests to get started.
    </div>
  {/if}
</div>
