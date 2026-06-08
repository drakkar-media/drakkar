<script lang="ts">
  import '../app.css';
  import AppShell from '$lib/components/AppShell.svelte';
  import ToastViewport from '$lib/components/ToastViewport.svelte';
</script>

<AppShell>
  <slot />
</AppShell>

<ToastViewport />
