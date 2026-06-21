<script lang="ts">
  import Tv from '@lucide/svelte/icons/tv';
  import Plus from '@lucide/svelte/icons/plus';
  import { detailsHref } from '$lib/detailsHref';
  import { itemStatus } from '$lib/itemStatus';
  import type { LibraryItem } from '$lib/types';

  export let item: LibraryItem;
  export let href = '';
  export let compact = false;
  export let showStatus = true;
  export let onRequest: ((item: LibraryItem) => void) | null = null;

  $: notInLibrary = !item.id && !!item.tmdbId;

  const isTv = (i: LibraryItem) => i.mediaType === 'tv' || i.mediaType === 'episode';

  function episodeCode(item: LibraryItem): string {
    if (!isTv(item)) return '';
    if (!item.seasonNumber || !item.episodeNumber) return '';
    return `S${String(item.seasonNumber).padStart(2, '0')}E${String(item.episodeNumber).padStart(2, '0')}`;
  }

  function metaLine(item: LibraryItem): string {
    const bits: string[] = [];
    if (item.year) bits.push(String(item.year));
    const ep = episodeCode(item);
    if (ep) bits.push(ep);
    return bits.join(' · ');
  }

  function statusLabel(item: LibraryItem): string {
    const st = itemStatus(item);
    if (st === 'available') {
      if (item.mediaType === 'episode') {
        if (item.seasonNumber && item.episodeNumber) {
          // Specific single episode
          return `S${String(item.seasonNumber).padStart(2,'0')}E${String(item.episodeNumber).padStart(2,'0')}`;
        }
        // Season pack — if all tracked episodes are present show "Available"
        if ((item.missingCount ?? 0) === 0) return 'Available';
        // Still downloading some episodes
        const avCount = item.availableCount ?? 0;
        if (avCount > 0) return `${avCount} ep`;
        return 'Available';
      }
      return 'Available'; // movie
    }
    if (st === 'partial') {
      const av = item.availableCount ?? 0;
      const tot = av + (item.missingCount ?? 0);
      return tot > 0 ? `${av}/${tot} ep` : 'Partial';
    }
    if (st === 'unreleased') return 'Queued';
    if (st === 'active') {
      const qs = (item.queueState ?? '').replace(/_/g, ' ');
      return qs || 'Downloading';
    }
    return 'Missing';
  }

  $: status = itemStatus(item);
</script>

<a class="poster-card" class:compact href={href || ((item.tmdbId || item.imdbId) ? detailsHref(item) : `/library/${item.id}`)}>
  {#if showStatus}<div class="status-bar status-{status}"></div>{/if}

  <div class="poster-frame">
    {#if item.posterUrl}
      <img src={item.posterUrl} alt="" loading="lazy" draggable="false" />
    {:else}
      <div class="poster-fallback"><Tv size={24} /></div>
    {/if}
    {#if showStatus}
      <div class="status-badge status-badge-{status}">{statusLabel(item)}</div>
    {/if}
    {#if notInLibrary && onRequest}
      <button class="request-btn" on:click|preventDefault|stopPropagation={() => onRequest && onRequest(item)} title="Request this title">
        <Plus size={14} />
      </button>
    {/if}
  </div>

  <div class="poster-copy">
    <div class="poster-title">{item.title}</div>
    {#if metaLine(item)}
      <div class="poster-meta">{metaLine(item)}</div>
    {/if}
  </div>
</a>

<style>
  .poster-card {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-width: 0;
    overflow: hidden;
    border-radius: var(--radius-lg, 0.75rem);
    border: 1px solid hsl(0 0% 100% / 0.07);
    background: hsl(var(--card) / 0.9);
    transition: transform 0.18s ease, border-color 0.18s ease, box-shadow 0.18s ease;
    text-decoration: none;
    cursor: pointer;
  }

  .poster-card:hover {
    transform: translateY(-3px) scale(1.01);
    border-color: hsl(var(--primary) / 0.4);
    box-shadow: 0 16px 40px hsl(171 82% 10% / 0.3);
  }

  /* 2px status bar at top */
  .status-bar {
    height: 2px;
    flex-shrink: 0;
    width: 100%;
  }
  .status-available   { background: hsl(var(--status-available)); }
  .status-partial     { background: hsl(var(--status-partial)); }
  .status-active      { background: hsl(var(--status-downloading)); }
  .status-unreleased  { background: hsl(var(--status-unreleased)); }
  .status-missing     { background: hsl(var(--status-missing)); }
  .status-failed      { background: hsl(var(--status-failed)); }

  /* Poster image */
  .poster-frame {
    position: relative;
    aspect-ratio: 2 / 3;
    flex-shrink: 0;
    background: hsl(var(--muted));
    overflow: hidden;
  }

  img {
    width: 100%;
    height: 100%;
    object-fit: cover;
    display: block;
  }

  .poster-fallback {
    width: 100%;
    height: 100%;
    display: grid;
    place-items: center;
    color: hsl(var(--muted-foreground));
  }

  /* Status badge — bottom-left overlay on poster */
  .status-badge {
    position: absolute;
    bottom: 6px;
    left: 6px;
    padding: 2px 7px;
    border-radius: 6px;
    font-size: 10px;
    font-weight: 700;
    font-family: 'JetBrains Mono', monospace;
    letter-spacing: 0.05em;
    text-transform: uppercase;
    backdrop-filter: blur(8px);
    border: 1px solid hsl(0 0% 0% / 0.2);
  }
  .status-badge-available   { background: hsl(var(--status-available) / 0.85);   color: hsl(0 0% 100%); }
  .status-badge-partial     { background: hsl(var(--status-partial) / 0.85);      color: hsl(0 0% 100%); }
  .status-badge-active      { background: hsl(var(--status-downloading) / 0.85);  color: hsl(0 0% 100%); }
  .status-badge-unreleased  { background: hsl(var(--status-unreleased) / 0.85);   color: hsl(0 0% 100%); }
  .status-badge-missing     { background: hsl(var(--status-missing) / 0.8);       color: hsl(0 0% 100%); }
  .status-badge-failed      { background: hsl(var(--status-failed) / 0.85);       color: hsl(0 0% 100%); }

  /* Request button overlay — top-right corner */
  .request-btn {
    position: absolute;
    top: 6px;
    right: 6px;
    width: 26px;
    height: 26px;
    border-radius: 999px;
    border: 1px solid hsl(0 0% 100% / 0.2);
    background: hsl(var(--primary) / 0.85);
    color: hsl(var(--primary-foreground));
    display: grid;
    place-items: center;
    cursor: pointer;
    backdrop-filter: blur(6px);
    transition: background 0.15s, transform 0.15s;
  }
  .request-btn:hover {
    background: hsl(var(--primary));
    transform: scale(1.1);
  }

  /* Title + meta */
  .poster-copy {
    padding: 8px 10px 10px;
    display: flex;
    flex-direction: column;
    gap: 3px;
    flex: 1;
  }

  .poster-title {
    font-size: 12px;
    font-weight: 600;
    line-height: 1.35;
    overflow: hidden;
    display: -webkit-box;
    line-clamp: 2;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
    color: hsl(var(--foreground));
  }

  .poster-meta {
    font-size: 11px;
    color: hsl(var(--muted-foreground));
    font-weight: 500;
    font-family: 'JetBrains Mono', monospace;
    letter-spacing: 0.01em;
  }

  .compact .poster-copy { padding: 6px 8px 8px; }
  .compact .poster-title { font-size: 11px; }
</style>
