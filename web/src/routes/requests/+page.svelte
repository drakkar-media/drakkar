<script lang="ts">
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Play from '@lucide/svelte/icons/play';
  import Button from '$lib/components/Button.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { dateTime, sentence } from '$lib/format';
  import { toastError, toastSuccess } from '$lib/toast';
  import type { RequestItem, Status } from '$lib/types';

  let status: Status | null = null;
  let requests: RequestItem[] = [];
  let loading = true;
  let working = false;
  let errorMessage = '';
  let infoMessage = '';

  async function loadRequests() {
    loading = true;
    errorMessage = '';
    try {
      const [statusResult, result] = await Promise.all([api.status(), api.requests()]);
      status = statusResult;
      requests = result.requests;
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
      toastError(errorMessage);
    } finally {
      loading = false;
    }
  }

  async function syncRequests() {
    working = true;
    errorMessage = '';
    infoMessage = '';
    try {
      const result = await api.syncRequests();
      infoMessage = `sync seen=${result.seen} created=${result.created}`;
      toastSuccess(infoMessage);
      await loadRequests();
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
      toastError(errorMessage);
    } finally {
      working = false;
    }
  }

  async function processPending() {
    working = true;
    errorMessage = '';
    infoMessage = '';
    try {
      const result = await api.searchPendingLibrary();
      infoMessage = `processed=${result.processed} searched=${result.searched} selected=${result.selected} failed=${result.failed}`;
      toastSuccess(infoMessage);
      await loadRequests();
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
      toastError(errorMessage);
    } finally {
      working = false;
    }
  }

  onMount(() => {
    void loadRequests();
    const unsubscribe = subscribeEvents(() => {
      if (!working) {
        void loadRequests();
      }
    });
    const timer = window.setInterval(() => void loadRequests(), 30000);
    return () => {
      window.clearInterval(timer);
      unsubscribe();
    };
  });

  $: seerrReady = status?.integrations?.seerr?.configured ?? false;
  $: hydraReady = status?.integrations?.nzbhydra2?.configured ?? false;
</script>

<svelte:head>
  <title>Drakkar Requests</title>
</svelte:head>

<PageHeader title="Requests" subtitle="Imported Seerr request records and queue state.">
  <Button kind="secondary" on:click={loadRequests} disabled={loading || working}>
    <RefreshCw size={16} />
    Refresh
  </Button>
  <Button kind="primary" on:click={syncRequests} disabled={loading || working || !seerrReady}>
    <SearchCheck size={16} />
    Sync
  </Button>
  <Button kind="secondary" on:click={processPending} disabled={loading || working || !hydraReady}>
    <Play size={16} />
    Process Pending
  </Button>
</PageHeader>

{#if errorMessage}<div class="banner error">{errorMessage}</div>{/if}
{#if infoMessage}<div class="banner info">{infoMessage}</div>{/if}
{#if status && !seerrReady}
  <div class="banner warn">Request sync disabled: {status.integrations.seerr.detail}.</div>
{/if}
{#if status && !hydraReady}
  <div class="banner warn">Pending search disabled: {status.integrations.nzbhydra2.detail}.</div>
{/if}

<Panel title="Request Feed" subtitle="Sorted by creation time descending." flush>
  {#if requests.length > 0}
    <div class="request-table">
      <div class="thead">
        <span>Title</span>
        <span>Type</span>
        <span>Queue</span>
        <span>Created</span>
      </div>
      {#each requests as item}
        <div class="row">
          <span><strong>{item.title || item.externalId}</strong></span>
          <span>{item.requestType} · {item.mediaType}</span>
          <span><StatusPill tone={item.queueState === 'available' ? 'ok' : 'neutral'}>{sentence(item.queueState)}</StatusPill></span>
          <span>{dateTime(item.createdAt)}</span>
        </div>
      {/each}
    </div>
  {:else if loading}
    <div class="empty">Loading requests.</div>
  {:else}
    <div class="empty">No requests.</div>
  {/if}
</Panel>

<style>
  .banner {
    margin-bottom: 16px;
    padding: 12px 14px;
    border-radius: 16px;
    font-size: 14px;
  }

  .error {
    border: 1px solid hsl(0 72% 51% / 0.28);
    background: hsl(0 72% 51% / 0.12);
    color: hsl(0 96% 82%);
  }

  .info {
    border: 1px solid hsl(171 82% 55% / 0.2);
    background: hsl(171 82% 55% / 0.1);
    color: hsl(171 82% 82%);
  }

  .warn {
    border: 1px solid hsl(42 95% 55% / 0.28);
    background: hsl(42 95% 55% / 0.12);
    color: hsl(48 100% 84%);
  }

  .request-table {
    display: grid;
    gap: 10px;
  }

  .thead,
  .row {
    display: grid;
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) 130px minmax(0, 1fr);
    gap: 12px;
    align-items: center;
  }

  .thead {
    color: hsl(var(--muted-foreground));
    font-size: 12px;
    text-transform: uppercase;
    padding: 0 4px;
  }

  .row {
    padding: 14px;
    border: 1px solid hsl(0 0% 100% / 0.06);
    border-radius: 18px;
    background: hsl(0 0% 100% / 0.03);
  }

  strong {
    display: block;
  }

  .empty {
    color: hsl(var(--muted-foreground));
  }

  @media (max-width: 900px) {
    .thead,
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
