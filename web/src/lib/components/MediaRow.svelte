<script lang="ts">
  /**
   * Horizontally scrollable row of PosterCard items (e.g. "Recently Added",
   * "Trending Movies") with drag-to-scroll, mouse wheel, and keyboard
   * navigation, plus optional nav-chevron buttons and a "view all" link.
   */
  import ChevronLeft from '@lucide/svelte/icons/chevron-left';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import PosterCard from '$lib/components/PosterCard.svelte';
  import type { LibraryItem } from '$lib/types';

  export let title = '';
  export let subtitle = '';
  export let items: LibraryItem[] = [];
  export let href = '';
  export let linkLabel = 'View All';
  export let itemWidth = 140;
  /**
   * Forwarded to each PosterCard; called only when the viewer taps
   * "request" on a title that isn't in the library yet.
   */
  export let onRequest: ((item: LibraryItem) => void) | null = null;

  let scroller: HTMLDivElement | null = null;
  let dragging = false;
  let hasDragged = false;
  let startX = 0;
  let startScrollLeft = 0;

  function pageDelta() {
    if (!scroller) return 0;
    return Math.max(220, Math.floor(scroller.clientWidth * 0.8));
  }

  function scrollByDelta(delta: number) {
    if (!scroller) return;
    scroller.scrollBy({ left: delta, behavior: 'smooth' });
  }

  function onPointerDown(event: PointerEvent) {
    if (!scroller) return;
    dragging = true;
    hasDragged = false;
    startX = event.clientX;
    startScrollLeft = scroller.scrollLeft;
    // Capture is deferred to onPointerMove, acquired only once an actual
    // drag (>8px) is detected -- acquiring it here unconditionally on every
    // pointerdown retargets the browser's synthesized click event to the
    // scroller itself for EVERY interaction, including plain clicks, so a
    // nested PosterCard <a> never receives its own click and never
    // navigates. Confirmed via an isolated repro: with capture acquired
    // eagerly, event.target on click was the scroller div even though the
    // pointer never left the link; deferring capture until a real drag is
    // detected fixed it without affecting drag-scroll behavior.
  }

  function onPointerMove(event: PointerEvent) {
    if (!dragging || !scroller) return;
    const dx = event.clientX - startX;
    if (!hasDragged && Math.abs(dx) > 8) {
      hasDragged = true;
      scroller.setPointerCapture(event.pointerId);
    }
    scroller.scrollLeft = startScrollLeft - dx;
  }

  function onPointerUp(event: PointerEvent) {
    if (!scroller) return;
    dragging = false;
    if (scroller.hasPointerCapture(event.pointerId)) {
      scroller.releasePointerCapture(event.pointerId);
    }
  }

  function onClickCapture(event: MouseEvent) {
    if (hasDragged) {
      event.preventDefault();
      event.stopPropagation();
      hasDragged = false;
    }
  }

  function onWheel(event: WheelEvent) {
    if (!scroller) return;
    if (Math.abs(event.deltaY) <= Math.abs(event.deltaX)) return;
    event.preventDefault();
    scroller.scrollLeft += event.deltaY;
  }

  function onKeyDown(event: KeyboardEvent) {
    if (!scroller) return;
    if (event.key === 'ArrowLeft') {
      event.preventDefault();
      scrollByDelta(-pageDelta());
    } else if (event.key === 'ArrowRight') {
      event.preventDefault();
      scrollByDelta(pageDelta());
    } else if (event.key === 'Home') {
      event.preventDefault();
      scroller.scrollTo({ left: 0, behavior: 'smooth' });
    } else if (event.key === 'End') {
      event.preventDefault();
      scroller.scrollTo({ left: scroller.scrollWidth, behavior: 'smooth' });
    }
  }
</script>

<section class="media-row">
  <div class="media-row-head">
    <div>
      <h2 class="media-row-title">{title}</h2>
      {#if subtitle}
        <p class="media-row-subtitle">{subtitle}</p>
      {/if}
    </div>
    <div class="media-row-actions">
      <button class="media-row-nav-btn" type="button" aria-label={`Scroll ${title} left`} on:click={() => scrollByDelta(-pageDelta())}>
        <ChevronLeft size={16} />
      </button>
      <button class="media-row-nav-btn" type="button" aria-label={`Scroll ${title} right`} on:click={() => scrollByDelta(pageDelta())}>
        <ChevronRight size={16} />
      </button>
      {#if href}
        <a class="media-row-link" href={href}>{linkLabel}</a>
      {/if}
    </div>
  </div>
  {#if items.length === 0}
    <div class="media-row-empty">No items.</div>
  {:else}
    <div
      class="media-row-scroll drag-scroll"
      class:dragging
      bind:this={scroller}
      role="button"
      aria-label={title}
      tabindex="0"
      on:pointerdown={onPointerDown}
      on:pointermove={onPointerMove}
      on:pointerup={onPointerUp}
      on:pointercancel={onPointerUp}
      on:wheel={onWheel}
      on:click|capture={onClickCapture}
      on:keydown={onKeyDown}
    >
      {#each items as item}
        <div class="media-row-item" style={`width:${itemWidth}px`}>
          <PosterCard {item} showStatus={item.id !== 0} {onRequest} />
        </div>
      {/each}
    </div>
  {/if}
</section>
