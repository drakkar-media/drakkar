<script lang="ts">
  /**
   * Prev/next (and optional first/last) pager for a numbered page range.
   * Renders nothing when there's only one page. Stateless: dispatches a
   * `change` event with the target page number and relies on the caller to
   * update the `page` prop in response.
   */
  import { createEventDispatcher } from 'svelte';

  export let page: number = 1;
  export let totalPages: number = 1;
  export let showFirstLast: boolean = true;

  const dispatch = createEventDispatcher<{ change: number }>();

  function go(n: number) {
    const target = Math.max(1, Math.min(totalPages, n));
    if (target !== page) dispatch('change', target);
  }
</script>

{#if totalPages > 1}
  <div class="pagination">
    {#if showFirstLast}
      <button class="pg-btn" disabled={page <= 1} on:click={() => go(1)}>«</button>
    {/if}
    <button class="pg-btn" disabled={page <= 1} on:click={() => go(page - 1)}>‹</button>
    <span class="pg-info">Page {page} of {totalPages}</span>
    <button class="pg-btn" disabled={page >= totalPages} on:click={() => go(page + 1)}>›</button>
    {#if showFirstLast}
      <button class="pg-btn" disabled={page >= totalPages} on:click={() => go(totalPages)}>»</button>
    {/if}
  </div>
{/if}

<style>
  .pagination { display: flex; align-items: center; gap: 6px; }
  .pg-btn {
    padding: 3px 10px; border-radius: var(--radius-md, 8px); font-size: 13px; cursor: pointer;
    background: var(--background); border: 1px solid var(--border);
    color: var(--foreground); transition: background 0.15s;
  }
  .pg-btn:hover:not(:disabled) { background: var(--muted); }
  .pg-btn:disabled { opacity: 0.35; cursor: default; }
  .pg-info { font-size: 13px; color: var(--muted-foreground); padding: 0 4px; }
</style>
