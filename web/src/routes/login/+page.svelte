<script lang="ts">
  /**
   * Renders the sign-in form and authenticates against the session cookie API.
   *
   * Posts credentials to `/api/auth/login`; on success it redirects to the
   * dashboard, replacing the current history entry so the login page is not
   * reachable via back navigation.
   */
  import { goto } from '$app/navigation';
  import DrakkarLogo from '$lib/components/DrakkarLogo.svelte';
  import Button from '$lib/components/Button.svelte';

  let username = '';
  let password = '';
  let error = '';
  let loading = false;

  async function login() {
    error = '';
    loading = true;
    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      });
      if (!res.ok) {
        error = 'Invalid username or password.';
        return;
      }
      await goto('/dashboard', { replaceState: true });
    } catch {
      error = 'Connection error. Please try again.';
    } finally {
      loading = false;
    }
  }
</script>

<svelte:head><title>Sign in — Drakkar</title></svelte:head>

<div class="flex min-h-screen items-center justify-center p-6">
  <div class="flex w-full max-w-95 flex-col gap-7 rounded-2xl border border-border bg-card/90 p-9">
    <div class="flex flex-col items-center gap-2.5 text-center">
      <div class="grid size-13 place-items-center rounded-2xl bg-primary text-primary-foreground"><DrakkarLogo size={28} /></div>
      <h1 class="m-0 text-[22px] font-bold tracking-[-0.01em]">Drakkar</h1>
      <p class="m-0 text-sm text-muted-foreground">Sign in to continue</p>
    </div>

    <form class="flex flex-col gap-4" on:submit|preventDefault={login}>
      <div class="flex flex-col gap-1.5">
        <label class="text-sm font-semibold text-muted-foreground" for="username">Username</label>
        <input id="username" type="text" bind:value={username} autocomplete="username" required />
      </div>
      <div class="flex flex-col gap-1.5">
        <label class="text-sm font-semibold text-muted-foreground" for="password">Password</label>
        <input id="password" type="password" bind:value={password} autocomplete="current-password" required />
      </div>

      {#if error}
        <p class="m-0 text-sm" style="color: hsl(var(--danger))">{error}</p>
      {/if}

      <Button kind="primary" type="submit" disabled={loading}>
        {loading ? 'Signing in…' : 'Sign in'}
      </Button>
    </form>
  </div>
</div>
