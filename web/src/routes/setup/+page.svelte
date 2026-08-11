<script lang="ts">
  /**
   * Displays the first-run setup form for creating Drakkar's initial admin account.
   *
   * Shown only when no admin user exists yet; on success it replaces history so the
   * setup page can't be reached again via the back button.
   */
  import { goto } from '$app/navigation';
  import DrakkarLogo from '$lib/components/DrakkarLogo.svelte';
  import Button from '$lib/components/Button.svelte';

  let username = '';
  let password = '';
  let confirm = '';
  let error = '';
  let loading = false;

  $: passwordMismatch = confirm !== '' && password !== confirm;
  $: canSubmit = username.trim() !== '' && password.length >= 8 && !passwordMismatch && !loading;

  /** Submits the new admin credentials to the setup API and redirects to the dashboard. */
  async function complete() {
    error = '';
    if (password !== confirm) { error = 'Passwords do not match.'; return; }
    loading = true;
    try {
      const res = await fetch('/api/setup/complete', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username: username.trim(), password }),
      });
      if (!res.ok) {
        const text = await res.text();
        try { error = JSON.parse(text).error; } catch { error = text || 'Setup failed.'; }
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

<svelte:head><title>Setup — Drakkar</title></svelte:head>

<div class="flex min-h-screen items-center justify-center p-6">
  <div class="flex w-full max-w-105 flex-col gap-7 rounded-2xl border border-border bg-card/90 p-9">
    <div class="flex flex-col items-center gap-2.5 text-center">
      <div class="grid size-13 place-items-center rounded-2xl bg-primary text-primary-foreground"><DrakkarLogo size={28} /></div>
      <h1 class="m-0 text-xl font-bold tracking-[-0.01em]">Welcome to Drakkar</h1>
      <p class="m-0 max-w-80 text-sm leading-relaxed text-muted-foreground">Create your admin account to get started. You can configure NNTP providers, indexers, and media servers in Settings afterwards.</p>
    </div>

    <form class="flex flex-col gap-4" on:submit|preventDefault={complete}>
      <div class="flex flex-col gap-1.5">
        <label class="text-sm font-semibold text-muted-foreground" for="username">Username</label>
        <input id="username" type="text" bind:value={username} autocomplete="username" required minlength="1" />
      </div>
      <div class="flex flex-col gap-1.5">
        <label class="flex items-baseline gap-1.5 text-sm font-semibold text-muted-foreground" for="password">Password <span class="text-xs font-normal">(min. 8 characters)</span></label>
        <input id="password" type="password" bind:value={password} autocomplete="new-password" required minlength="8" />
      </div>
      <div class="flex flex-col gap-1.5">
        <label class="text-sm font-semibold text-muted-foreground" for="confirm">Confirm password</label>
        <input
          id="confirm"
          type="password"
          bind:value={confirm}
          autocomplete="new-password"
          style={passwordMismatch ? 'border-color: hsl(var(--danger) / 0.7)' : undefined}
          required
        />
        {#if passwordMismatch}<span class="text-xs" style="color: hsl(var(--danger))">Passwords do not match.</span>{/if}
      </div>

      {#if error}
        <p class="m-0 text-sm" style="color: hsl(var(--danger))">{error}</p>
      {/if}

      <Button kind="primary" type="submit" disabled={!canSubmit}>
        {loading ? 'Creating account…' : 'Create account & continue'}
      </Button>
    </form>
  </div>
</div>
