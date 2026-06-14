<script lang="ts">
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
    padding: 3px 10px; border-radius: 8px; font-size: 13px; cursor: pointer;
    background: hsl(0 0% 100% / 0.06); border: 1px solid hsl(0 0% 100% / 0.1);
    color: hsl(var(--foreground)); transition: background 0.15s;
  }
  .pg-btn:hover:not(:disabled) { background: hsl(0 0% 100% / 0.12); }
  .pg-btn:disabled { opacity: 0.35; cursor: default; }
  .pg-info { font-size: 13px; color: hsl(var(--muted-foreground)); padding: 0 4px; }
</style>
