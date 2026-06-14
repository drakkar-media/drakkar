<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import '../app.css';
  import AppShell from '$lib/components/AppShell.svelte';
  import ToastViewport from '$lib/components/ToastViewport.svelte';

  const PUBLIC_PATHS = ['/login', '/setup'];

  let ready = false;

  $: isPublic = PUBLIC_PATHS.some((p) => page.url.pathname.startsWith(p));

  onMount(async () => {
    if (isPublic) {
      ready = true;
      return;
    }

    // Check if first-time setup is still required.
    try {
      const setupRes = await fetch('/api/setup/status');
      if (setupRes.ok) {
        const setup = await setupRes.json();
        if (setup.required) {
          await goto('/setup', { replaceState: true });
          return;
        }
      }
    } catch {
      // ignore — if setup check fails, try auth
    }

    // Verify the session cookie is valid.
    const meRes = await fetch('/api/auth/me');
    if (!meRes.ok) {
      await goto('/login', { replaceState: true });
      return;
    }

    ready = true;
  });
</script>

{#if isPublic}
  <slot />
  <ToastViewport />
{:else if ready}
  <AppShell>
    <slot />
  </AppShell>
  <ToastViewport />
{/if}
