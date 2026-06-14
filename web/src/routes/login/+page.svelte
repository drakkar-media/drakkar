<script lang="ts">
  import { goto } from '$app/navigation';
  import DrakkarLogo from '$lib/components/DrakkarLogo.svelte';

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

<div class="page">
  <div class="card">
    <div class="brand">
      <div class="logo"><DrakkarLogo size={28} /></div>
      <h1>Drakkar</h1>
      <p>Sign in to continue</p>
    </div>

    <form on:submit|preventDefault={login}>
      <div class="field">
        <label for="username">Username</label>
        <input id="username" type="text" bind:value={username} autocomplete="username" required />
      </div>
      <div class="field">
        <label for="password">Password</label>
        <input id="password" type="password" bind:value={password} autocomplete="current-password" required />
      </div>

      {#if error}
        <p class="err">{error}</p>
      {/if}

      <button type="submit" class="btn" disabled={loading}>
        {loading ? 'Signing in…' : 'Sign in'}
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
    max-width: 380px;
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
    font-size: 22px;
    font-weight: 700;
    letter-spacing: -0.01em;
  }

  .brand p {
    margin: 0;
    color: hsl(var(--muted-foreground));
    font-size: 14px;
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
    opacity: 0.6;
    cursor: default;
  }

  .btn:not(:disabled):hover {
    opacity: 0.88;
  }
</style>
