<script lang="ts">
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Shield from '@lucide/svelte/icons/shield';
  import Trash2 from '@lucide/svelte/icons/trash-2';
  import UserPlus from '@lucide/svelte/icons/user-plus';
  import KeyRound from '@lucide/svelte/icons/key-round';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import type { APIToken, User } from '$lib/types';

  let users: User[] = [];
  let me: User | null = null;
  let tokens: APIToken[] = [];
  let loading = true;
  let busy: Record<string, boolean> = {};
  function isBusy(key: string): boolean {
    return !!busy[key];
  }
  function setBusy(key: string, value: boolean) {
    busy = { ...busy, [key]: value };
  }

  let username = '';
  let password = '';
  let role = 'admin';
  let passwordDrafts: Record<number, string> = {};
  let tokenName = '';
  let tokenExpiresAt = '';
  let createdToken = '';

  async function load() {
    loading = true;
    try {
      const [nextUsers, nextMe, nextTokens] = await Promise.all([api.listUsers(), api.me(), api.listApiTokens()]);
      users = nextUsers;
      me = nextMe;
      tokens = nextTokens;
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      loading = false;
    }
  }

  async function createUser() {
    if (!username.trim() || password.length < 8) return;
    setBusy('create-user', true);
    try {
      await api.createUser(username.trim(), password, role);
      toastSuccess(`User ${username.trim()} created`);
      username = '';
      password = '';
      role = 'admin';
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy('create-user', false);
    }
  }

  async function deleteUser(id: number, name: string) {
    if (me?.id === id) {
      toastError('You cannot delete your own account');
      return;
    }
    if (typeof window !== 'undefined' && !window.confirm(`Delete user "${name}"?`)) return;
    setBusy(`delete-user-${id}`, true);
    try {
      const res = await api.deleteUser(id);
      if (!res.ok) throw new Error(await res.text() || 'delete failed');
      toastSuccess(`User ${name} deleted`);
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(`delete-user-${id}`, false);
    }
  }

  async function changePassword(id: number, name: string) {
    const next = passwordDrafts[id]?.trim() ?? '';
    if (next.length < 8) {
      toastError('Password must be at least 8 characters');
      return;
    }
    setBusy(`change-password-${id}`, true);
    try {
      const res = await api.changePassword(id, next);
      if (!res.ok) throw new Error(await res.text() || 'password change failed');
      passwordDrafts = { ...passwordDrafts, [id]: '' };
      toastSuccess(`Password updated for ${name}`);
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(`change-password-${id}`, false);
    }
  }

  async function createToken() {
    if (!tokenName.trim()) return;
    setBusy('create-token', true);
    try {
      const created = await api.createApiToken(tokenName.trim(), tokenExpiresAt ? new Date(tokenExpiresAt).toISOString() : null);
      createdToken = created.token;
      tokenName = '';
      tokenExpiresAt = '';
      toastSuccess(`API token ${created.name} created`);
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy('create-token', false);
    }
  }

  async function deleteToken(id: number, name: string) {
    if (typeof window !== 'undefined' && !window.confirm(`Delete API token "${name}"?`)) return;
    setBusy(`delete-token-${id}`, true);
    try {
      const res = await api.deleteApiToken(id);
      if (!res.ok) throw new Error(await res.text() || 'delete failed');
      toastSuccess(`API token ${name} deleted`);
      await load();
    } catch (error) {
      toastError(error instanceof Error ? error.message : String(error));
    } finally {
      setBusy(`delete-token-${id}`, false);
    }
  }

  onMount(() => {
    void load();
  });
</script>

<svelte:head><title>Users — Drakkar</title></svelte:head>

<PageHeader title="Users" subtitle="Manage operator accounts, roles, and passwords for Drakkar.">
  <Button kind="secondary" on:click={load} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
</PageHeader>

<section class="summary-grid">
  <div class="summary-card">
    <div class="summary-value">{users.length}</div>
    <div class="summary-label">Total users</div>
  </div>
  <div class="summary-card">
    <div class="summary-value">{users.filter((user) => user.role === 'admin').length}</div>
    <div class="summary-label">Admins</div>
  </div>
  <div class="summary-card">
    <div class="summary-value">{me?.username ?? '—'}</div>
    <div class="summary-label">Current session</div>
  </div>
</section>

<div class="grid">
  <div class="sidebar-stack">
    <Panel title="Create User" subtitle="Adds a new local account and immediately makes it available for login.">
      <form class="create-form" on:submit|preventDefault={createUser}>
        <label>
          <span>Username</span>
          <input bind:value={username} type="text" autocomplete="off" placeholder="operator" />
        </label>
        <label>
          <span>Password</span>
          <input bind:value={password} type="password" autocomplete="new-password" placeholder="minimum 8 characters" />
        </label>
        <label>
          <span>Role</span>
          <select bind:value={role}>
            <option value="admin">Admin</option>
            <option value="user">User</option>
          </select>
        </label>
        <Button kind="primary" disabled={isBusy('create-user') || !username.trim() || password.length < 8}>
          <UserPlus size={14} />
          Create User
        </Button>
      </form>
    </Panel>

    <Panel title="API Tokens" subtitle="Personal access tokens for scripts and automation. The raw token is shown only once after creation.">
      <form class="create-form" on:submit|preventDefault={createToken}>
        <label>
          <span>Name</span>
          <input bind:value={tokenName} type="text" autocomplete="off" placeholder="home-lab-sync" />
        </label>
        <label>
          <span>Expires At</span>
          <input bind:value={tokenExpiresAt} type="datetime-local" />
        </label>
        <Button kind="primary" disabled={isBusy('create-token') || !tokenName.trim()}>
          <Shield size={14} />
          Create Token
        </Button>
      </form>

      {#if createdToken}
        <div class="token-reveal" role="status" aria-live="polite">
          <div class="token-reveal-title">Copy this token now</div>
          <code>{createdToken}</code>
        </div>
      {/if}

      {#if tokens.length > 0}
        <div class="token-list">
          {#each tokens as token}
            <div class="token-card">
              <div>
                <div class="user-name">{token.name}</div>
                <div class="user-meta">
                  Created {new Date(token.createdAt).toLocaleString('en-GB')}
                  {#if token.lastUsedAt}
                    · Last used {new Date(token.lastUsedAt).toLocaleString('en-GB')}
                  {/if}
                  {#if token.expiresAt}
                    · Expires {new Date(token.expiresAt).toLocaleString('en-GB')}
                  {/if}
                </div>
              </div>
              <Button kind="danger" on:click={() => deleteToken(token.id, token.name)} disabled={isBusy(`delete-token-${token.id}`)}>
                <Trash2 size={14} />
                Delete
              </Button>
            </div>
          {/each}
        </div>
      {:else if !loading}
        <div class="empty">No API tokens created yet.</div>
      {/if}
    </Panel>
  </div>

  <Panel title="Accounts" subtitle="Current users with password rotation and delete controls.">
    {#if users.length > 0}
      <div class="user-list">
        {#each users as user}
          <div class="user-card">
            <div class="user-head">
              <div>
                <div class="user-name">{user.username}</div>
                <div class="user-meta">Created {new Date(user.createdAt).toLocaleString('en-GB')}</div>
              </div>
              <div class="user-badges">
                <StatusPill tone={user.role === 'admin' ? 'ok' : 'neutral'}>{user.role}</StatusPill>
                {#if me?.id === user.id}
                  <StatusPill tone="neutral">current</StatusPill>
                {/if}
              </div>
            </div>

            <div class="password-row">
              <label class="password-field">
                <span>New password</span>
                <input
                  bind:value={passwordDrafts[user.id]}
                  type="password"
                  autocomplete="new-password"
                  placeholder="minimum 8 characters"
                />
              </label>
              <Button kind="secondary" on:click={() => changePassword(user.id, user.username)} disabled={isBusy(`change-password-${user.id}`) || (passwordDrafts[user.id]?.length ?? 0) < 8}>
                <KeyRound size={14} />
                Change Password
              </Button>
              <Button kind="danger" on:click={() => deleteUser(user.id, user.username)} disabled={isBusy(`delete-user-${user.id}`) || me?.id === user.id}>
                <Trash2 size={14} />
                Delete
              </Button>
            </div>
          </div>
        {/each}
      </div>
    {:else if loading}
      <div class="empty">Loading users…</div>
    {:else}
      <div class="empty">No users found.</div>
    {/if}
  </Panel>
</div>

<style>
  .summary-grid {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 14px;
    margin-bottom: 20px;
  }

  .summary-card,
  .user-card {
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 20px;
    background: hsl(var(--card) / 0.82);
  }

  .summary-card {
    padding: 18px 20px;
  }

  .summary-value {
    font-size: 1.8rem;
    font-weight: 700;
    line-height: 1;
  }

  .summary-label,
  .user-meta,
  .empty {
    margin-top: 8px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .grid {
    display: grid;
    grid-template-columns: minmax(320px, 420px) minmax(0, 1fr);
    gap: 16px;
  }

  .sidebar-stack {
    display: grid;
    gap: 16px;
  }

  .create-form,
  .user-list,
  .token-list {
    display: grid;
    gap: 12px;
  }

  label,
  .password-field {
    display: grid;
    gap: 6px;
  }

  label span,
  .password-field span {
    font-size: 13px;
    font-weight: 600;
  }

  input,
  select {
    min-height: 40px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    padding: 0 12px;
    font-size: 13px;
  }

  .user-card {
    padding: 16px 18px;
  }

  .user-head,
  .user-badges,
  .password-row {
    display: flex;
    align-items: center;
    gap: 12px;
  }

  .user-head {
    justify-content: space-between;
    margin-bottom: 14px;
  }

  .user-name {
    font-weight: 700;
    font-size: 15px;
  }

  .user-badges {
    flex-wrap: wrap;
    justify-content: flex-end;
  }

  .password-row {
    flex-wrap: wrap;
  }

  .password-field {
    flex: 1 1 260px;
  }

  .token-card,
  .token-reveal {
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 16px;
    background: hsl(0 0% 100% / 0.03);
    padding: 14px;
  }

  .token-card {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 12px;
  }

  .token-reveal-title {
    margin-bottom: 8px;
    font-size: 13px;
    font-weight: 700;
  }

  .token-reveal code {
    display: block;
    overflow-wrap: anywhere;
  }

  @media (max-width: 900px) {
    .summary-grid,
    .grid {
      grid-template-columns: 1fr;
    }
  }
</style>
