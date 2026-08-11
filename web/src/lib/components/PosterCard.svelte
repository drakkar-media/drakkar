<script lang="ts">
  /**
   * Poster-first card for a library item or discover result: image, status
   * bar/badge, title, and year/episode meta. Links to the item's detail
   * page (or library entry) unless `href` overrides it, and can offer a
   * quick "request" action for titles not yet in the library.
   */
  import Tv from '@lucide/svelte/icons/tv';
  import Plus from '@lucide/svelte/icons/plus';
  import { detailsHref } from '$lib/detailsHref';
  import { itemStatus } from '$lib/itemStatus';
  import type { LibraryItem } from '$lib/types';

  export let item: LibraryItem;
  export let href = '';
  /** Denser layout (smaller padding/title size), for use in tight rows/grids. */
  export let compact = false;
  /** Hides the status bar/badge overlay, e.g. for placeholder items with no queue state. */
  export let showStatus = true;
  /** Shows a "request" button; fires only for items not yet in the library (no `id`, but has a `tmdbId`). */
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

<a
  class="poster-card"
  href={href || ((item.tmdbId || item.imdbId) ? detailsHref(item) : `/library/${item.id}`)}
>
  {#if showStatus}
    <div class={`poster-status-bar poster-status-bar-${status}`}></div>
  {/if}

  <div class="poster-frame">
    {#if item.posterUrl}
      <img src={item.posterUrl} alt="" loading="lazy" draggable="false" />
    {:else}
      <div class="poster-fallback"><Tv size={24} /></div>
    {/if}
    {#if showStatus}
      <div class={`poster-badge poster-badge-${status}`}>{statusLabel(item)}</div>
    {/if}
    {#if notInLibrary && onRequest}
      <span
        class="poster-request-btn"
        role="button"
        tabindex="0"
        aria-label="Request this title"
        title="Request this title"
        on:click|preventDefault|stopPropagation={() => onRequest && onRequest(item)}
        on:keydown|preventDefault|stopPropagation={(e) => { if (e.key === 'Enter' || e.key === ' ') onRequest && onRequest(item); }}
      >
        <Plus size={14} />
      </span>
    {/if}
  </div>

  <div class="poster-copy" class:poster-copy-compact={compact}>
    <div class="poster-title" class:poster-title-compact={compact}>{item.title}</div>
    {#if metaLine(item)}
      <div class="poster-meta">{metaLine(item)}</div>
    {/if}
  </div>
</a>
