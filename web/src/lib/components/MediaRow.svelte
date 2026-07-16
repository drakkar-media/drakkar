<script lang="ts">
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
  <div class="row-head">
    <div>
      <h2 class="section-title">{title}</h2>
      {#if subtitle}
        <p class="row-subtitle">{subtitle}</p>
      {/if}
    </div>
    <div class="row-actions">
      <button class="nav-btn" type="button" aria-label={`Scroll ${title} left`} on:click={() => scrollByDelta(-pageDelta())}>
        <ChevronLeft size={16} />
      </button>
      <button class="nav-btn" type="button" aria-label={`Scroll ${title} right`} on:click={() => scrollByDelta(pageDelta())}>
        <ChevronRight size={16} />
      </button>
      {#if href}
        <a class="row-link" href={href}>{linkLabel}</a>
      {/if}
    </div>
  </div>
  {#if items.length === 0}
    <div class="row-empty">No items.</div>
  {:else}
    <div
      class="row-scroll drag-scroll"
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
        <div class="row-item" style={`width:${itemWidth}px`}>
          <PosterCard {item} compact showStatus={item.id !== 0} {onRequest} />
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .media-row {
    display: grid;
    gap: 16px;
  }

  .row-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .section-title {
    position: relative;
    margin: 0;
    padding-left: 14px;
    font-size: 24px;
    font-weight: 800;
  }

  .section-title::before {
    content: '';
    position: absolute;
    left: 0;
    top: 50%;
    width: 4px;
    height: 22px;
    transform: translateY(-50%);
    border-radius: 999px;
    background: hsl(var(--accent));
  }

  .row-link {
    padding: 10px 14px;
    border-radius: 999px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    font-weight: 700;
  }

  .row-actions {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }

  .row-subtitle {
    margin: 4px 0 0 14px;
    color: hsl(var(--muted-foreground));
    font-size: 12px;
  }

  .nav-btn {
    display: inline-grid;
    place-items: center;
    width: 34px;
    height: 34px;
    border-radius: 999px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    cursor: pointer;
  }

  .row-scroll {
    display: flex;
    gap: 12px;
    overflow-x: auto;
    padding-bottom: 6px;
    scroll-snap-type: x proximity;
  }

  .row-scroll.dragging {
    cursor: grabbing;
  }

  .row-item {
    flex: 0 0 auto;
    scroll-snap-align: start;
  }

  .row-empty {
    padding: 18px;
    border-radius: 18px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    background: hsl(0 0% 100% / 0.03);
    color: hsl(var(--muted-foreground));
  }

  @media (max-width: 700px) {
    .row-head {
      align-items: flex-start;
      flex-direction: column;
    }
  }
</style>
