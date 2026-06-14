<script lang="ts">
  import { goto } from '$app/navigation';
  import DrakkarLogo from '$lib/components/DrakkarLogo.svelte';

  let username = '';
  let password = '';
  let confirm = '';
  let error = '';
  let loading = false;

  $: passwordMismatch = confirm !== '' && password !== confirm;
  $: canSubmit = username.trim() !== '' && password.length >= 8 && !passwordMismatch && !loading;

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

<div class="page">
  <div class="card">
    <div class="brand">
      <div class="logo"><DrakkarLogo size={28} /></div>
      <h1>Welcome to Drakkar</h1>
      <p>Create your admin account to get started. You can configure NNTP providers, indexers, and media servers in Settings afterwards.</p>
    </div>

    <form on:submit|preventDefault={complete}>
      <div class="field">
        <label for="username">Username</label>
        <input id="username" type="text" bind:value={username} autocomplete="username" required minlength="1" />
      </div>
      <div class="field">
        <label for="password">Password <span class="hint">(min. 8 characters)</span></label>
        <input id="password" type="password" bind:value={password} autocomplete="new-password" required minlength="8" />
      </div>
      <div class="field">
        <label for="confirm">Confirm password</label>
        <input
          id="confirm"
          type="password"
          bind:value={confirm}
          autocomplete="new-password"
          class:invalid={passwordMismatch}
          required
        />
        {#if passwordMismatch}<span class="field-err">Passwords do not match.</span>{/if}
      </div>

      {#if error}
        <p class="err">{error}</p>
      {/if}

      <button type="submit" class="btn" disabled={!canSubmit}>
        {loading ? 'Creating account…' : 'Create account & continue'}
      </button>
    </form>
  </div>
</div>

<style>
  .page {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 24px;
  }

  .card {
    width: 100%;
    max-width: 420px;
    border: 1px solid hsl(var(--border) / 0.9);
    border-radius: 24px;
    background: hsl(var(--card) / 0.9);
    padding: 40px 36px;
    display: flex;
    flex-direction: column;
    gap: 28px;
    box-shadow: 0 24px 64px hsl(0 0% 0% / 0.4);
    backdrop-filter: blur(20px);
  }

  .brand {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    text-align: center;
  }

  .logo {
    width: 52px;
    height: 52px;
    border-radius: 16px;
    background: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
    display: grid;
    place-items: center;
  }

  .brand h1 {
    margin: 0;
    font-size: 20px;
    font-weight: 700;
    letter-spacing: -0.01em;
  }

  .brand p {
    margin: 0;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
    line-height: 1.55;
    max-width: 320px;
  }

  form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }

  .field {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  label {
    font-size: 13px;
    font-weight: 600;
    color: hsl(var(--muted-foreground));
    display: flex;
    gap: 6px;
    align-items: baseline;
  }

  .hint {
    font-weight: 400;
    font-size: 11px;
  }

  input {
    height: 44px;
    padding: 0 14px;
    border-radius: 14px;
    border: 1px solid hsl(var(--border) / 0.8);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    font-size: 14px;
    outline: none;
    transition: border-color 0.15s;
  }

  input:focus {
    border-color: hsl(var(--primary) / 0.6);
  }

  input.invalid {
    border-color: hsl(var(--danger) / 0.7);
  }

  .field-err {
    font-size: 12px;
    color: hsl(var(--danger));
  }

  .err {
    margin: 0;
    font-size: 13px;
    color: hsl(var(--danger));
  }

  .btn {
    height: 46px;
    border-radius: 14px;
    border: none;
    background: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
    font-size: 14px;
    font-weight: 700;
    cursor: pointer;
    transition: opacity 0.15s;
  }

  .btn:disabled {
    opacity: 0.5;
    cursor: default;
  }

  .btn:not(:disabled):hover {
    opacity: 0.88;
  }
</style>
