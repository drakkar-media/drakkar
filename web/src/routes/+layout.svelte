<script lang="ts">
  /**
   * Root layout wrapping every route: bootstraps first-run setup detection and
   * session authentication before any protected page renders.
   *
   * On mount (for non-public routes) it checks `/api/setup/status` and
   * redirects to `/setup` if first-time setup is still required, then verifies
   * the session cookie via `/api/auth/me` and redirects to `/login` on
   * failure. `/login` and `/setup` themselves render immediately without these
   * checks. Once checks pass, child routes are rendered inside `AppShell`
   * (nav/chrome) alongside the global `ToastViewport`.
   */
  import { onMount } from 'svelte';
  import { afterNavigate, goto } from '$app/navigation';
  import { page } from '$app/state';
  import '../app.css';
  import AppShell from '$lib/components/AppShell.svelte';
  import ToastViewport from '$lib/components/ToastViewport.svelte';

  const PUBLIC_PATHS = ['/login', '/setup'];

  let ready = false;

  // Reading page.url.pathname from a $: block does not reliably re-run on
  // client-side navigation in this app (confirmed: after logging in, this
  // stayed stuck on "true" for /login forever, so the app silently rendered
  // bare page content with no AppShell/sidebar at all -- never a visible
  // error, just missing chrome). afterNavigate + a plain `let`, plus an
  // eager synchronous read for the very first load (afterNavigate does not
  // fire for that one), is the reliable alternative used here and in
  // AppShell.
  let isPublic = PUBLIC_PATHS.some((p) => page.url.pathname.startsWith(p));
  afterNavigate((nav) => {
    const pathname = nav.to?.url.pathname ?? window.location.pathname;
    isPublic = PUBLIC_PATHS.some((p) => pathname.startsWith(p));
  });

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
