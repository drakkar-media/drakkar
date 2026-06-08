<script lang="ts">
  import { goto } from '$app/navigation';
  import { onMount } from 'svelte';

  onMount(() => {
    void goto('/downloads', { replaceState: true });
  });
</script>

<svelte:head><title>Redirecting — Drakkar</title></svelte:head>

<div style="padding:24px;color:hsl(var(--muted-foreground));">Redirecting to downloads…</div>
