<script lang="ts">
  /**
   * Displays operator account management (create/delete users, reset
   * passwords) and personal API token management.
   *
   * A newly-created token's raw secret is only ever available in `createdToken`
   * right after creation — the backend doesn't return it again afterwards.
   */
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
  import * as Select from '$lib/components/ui/select/index.js';
  import { api } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';
  import { runAction, confirmed } from '$lib/actions';
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
    const created = await runAction(() => api.createUser(username.trim(), password, role), {
      setWorking: (v) => setBusy('create-user', v),
      successMessage: () => `User ${username.trim()} created`,
      afterSuccess: load
    });
    if (created) {
      username = '';
      password = '';
      role = 'admin';
    }
  }

  async function deleteUser(id: number, name: string) {
    if (me?.id === id) {
      toastError('You cannot delete your own account');
      return;
    }
    if (!confirmed(`Delete user "${name}"?`)) return;
    await runAction(() => api.deleteUser(id), {
      setWorking: (v) => setBusy(`delete-user-${id}`, v),
      successMessage: () => `User ${name} deleted`,
      afterSuccess: load
    });
  }

  async function changePassword(id: number, name: string) {
    const next = passwordDrafts[id]?.trim() ?? '';
    if (next.length < 8) {
      toastError('Password must be at least 8 characters');
      return;
    }
    await runAction(() => api.changePassword(id, next), {
      setWorking: (v) => setBusy(`change-password-${id}`, v),
      successMessage: () => `Password updated for ${name}`,
      afterSuccess: () => {
        passwordDrafts = { ...passwordDrafts, [id]: '' };
      }
    });
  }

  async function createToken() {
    if (!tokenName.trim()) return;
    const created = await runAction(
      () => api.createApiToken(tokenName.trim(), tokenExpiresAt ? new Date(tokenExpiresAt).toISOString() : null),
      {
        setWorking: (v) => setBusy('create-token', v),
        successMessage: (r) => `API token ${r.name} created`,
        afterSuccess: load
      }
    );
    if (created) {
      createdToken = created.token;
      tokenName = '';
      tokenExpiresAt = '';
    }
  }

  async function deleteToken(id: number, name: string) {
    if (!confirmed(`Delete API token "${name}"?`)) return;
    await runAction(() => api.deleteApiToken(id), {
      setWorking: (v) => setBusy(`delete-token-${id}`, v),
      successMessage: () => `API token ${name} deleted`,
      afterSuccess: load
    });
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

<section class="mb-5 grid grid-cols-1 gap-3.5 sm:grid-cols-3">
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-xl font-bold leading-none">{users.length}</div>
    <div class="mt-2 text-sm text-muted-foreground">Total users</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-xl font-bold leading-none">{users.filter((user) => user.role === 'admin').length}</div>
    <div class="mt-2 text-sm text-muted-foreground">Admins</div>
  </div>
  <div class="rounded-xl border border-border bg-card/80 px-4 py-3.5">
    <div class="text-xl font-bold leading-none">{me?.username ?? '—'}</div>
    <div class="mt-2 text-sm text-muted-foreground">Current session</div>
  </div>
</section>

<div class="grid grid-cols-1 gap-4 md:grid-cols-[minmax(320px,420px)_minmax(0,1fr)]">
  <div class="grid gap-4">
    <Panel title="Create User" subtitle="Adds a new local account and immediately makes it available for login.">
      <form class="grid gap-3" on:submit|preventDefault={createUser}>
        <label class="grid gap-1.5">
          <span class="text-sm font-semibold">Username</span>
          <input bind:value={username} type="text" autocomplete="off" placeholder="operator" />
        </label>
        <label class="grid gap-1.5">
          <span class="text-sm font-semibold">Password</span>
          <input bind:value={password} type="password" autocomplete="new-password" placeholder="minimum 8 characters" />
        </label>
        <label class="grid gap-1.5">
          <span class="text-sm font-semibold">Role</span>
          <Select.Root type="single" bind:value={role}>
            <Select.Trigger class="w-full">{role === 'admin' ? 'Admin' : 'User'}</Select.Trigger>
            <Select.Content>
              <Select.Item value="admin">Admin</Select.Item>
              <Select.Item value="user">User</Select.Item>
            </Select.Content>
          </Select.Root>
        </label>
        <Button kind="primary" disabled={isBusy('create-user') || !username.trim() || password.length < 8}>
          <UserPlus size={14} />
          Create User
        </Button>
      </form>
    </Panel>

    <Panel title="API Tokens" subtitle="Personal access tokens for scripts and automation. The raw token is shown only once after creation.">
      <form class="grid gap-3" on:submit|preventDefault={createToken}>
        <label class="grid gap-1.5">
          <span class="text-sm font-semibold">Name</span>
          <input bind:value={tokenName} type="text" autocomplete="off" placeholder="home-lab-sync" />
        </label>
        <label class="grid gap-1.5">
          <span class="text-sm font-semibold">Expires At</span>
          <input bind:value={tokenExpiresAt} type="datetime-local" />
        </label>
        <Button kind="primary" disabled={isBusy('create-token') || !tokenName.trim()}>
          <Shield size={14} />
          Create Token
        </Button>
      </form>

      {#if createdToken}
        <div class="mt-3 rounded-2xl border border-border bg-muted/30 p-3.5" role="status" aria-live="polite">
          <div class="mb-2 text-sm font-bold">Copy this token now</div>
          <code class="block break-all">{createdToken}</code>
        </div>
      {/if}

      {#if tokens.length > 0}
        <div class="mt-3 grid gap-3">
          {#each tokens as token}
            <div class="flex items-center justify-between gap-3 rounded-2xl border border-border bg-muted/30 p-3.5">
              <div>
                <div class="text-[15px] font-bold">{token.name}</div>
                <div class="mt-2 text-sm text-muted-foreground">
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
        <div class="mt-3 text-sm text-muted-foreground">No API tokens created yet.</div>
      {/if}
    </Panel>
  </div>

  <Panel title="Accounts" subtitle="Current users with password rotation and delete controls.">
    {#if users.length > 0}
      <div class="grid gap-3">
        {#each users as user}
          <div class="rounded-xl border border-border bg-card/80 px-4.5 py-4">
            <div class="mb-3.5 flex items-center justify-between gap-3">
              <div>
                <div class="text-[15px] font-bold">{user.username}</div>
                <div class="mt-2 text-sm text-muted-foreground">Created {new Date(user.createdAt).toLocaleString('en-GB')}</div>
              </div>
              <div class="flex flex-wrap items-center justify-end gap-3">
                <StatusPill tone={user.role === 'admin' ? 'ok' : 'neutral'}>{user.role}</StatusPill>
                {#if me?.id === user.id}
                  <StatusPill tone="neutral">current</StatusPill>
                {/if}
              </div>
            </div>

            <div class="flex flex-wrap items-center gap-3">
              <label class="grid flex-1 basis-64 gap-1.5">
                <span class="text-sm font-semibold">New password</span>
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
      <div class="text-sm text-muted-foreground">Loading users…</div>
    {:else}
      <div class="text-sm text-muted-foreground">No users found.</div>
    {/if}
  </Panel>
</div>
