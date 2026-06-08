<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { onMount } from 'svelte';
  import { api } from '$lib/api';
  import { detailsHref } from '$lib/detailsHref';
  import { toastError } from '$lib/toast';

  let loading = true;
  let errorMessage = '';

  async function redirectToMergedDetail() {
    loading = true;
    errorMessage = '';
    try {
      const libraryItemID = Number(page.params.id);
      const detail = await api.libraryDetail(libraryItemID);
      const href = detailsHref({
        mediaType: detail.mediaType,
        title: detail.title,
        year: detail.year,
        tmdbId: detail.tmdbId,
        imdbId: detail.imdbId
      });
      await goto(href, { replaceState: true });
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
      toastError(errorMessage);
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    void redirectToMergedDetail();
  });
</script>

<svelte:head><title>Redirecting… — Drakkar</title></svelte:head>

{#if loading}
  <div class="empty">Opening merged details…</div>
{:else if errorMessage}
  <div class="empty">{errorMessage}</div>
{/if}

<style>
  .empty {
    padding: 28px; text-align: center; color: hsl(var(--muted-foreground));
    border-radius: 20px; border: 1px solid hsl(0 0% 100% / 0.06); background: hsl(0 0% 100% / 0.02);
  }
</style>
