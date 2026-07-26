<script lang="ts">
  /**
   * Redirects immediately to /downloads — the queue view now lives there.
   * Uses replaceState so this redirect page doesn't linger in browser history.
   */
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  onMount(() => {
    void goto('/downloads', { replaceState: true });
  });
</script>

<svelte:head><title>Redirecting — Drakkar</title></svelte:head>

<div style="padding:24px;color:hsl(var(--muted-foreground));">Redirecting to downloads…</div>
