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
  import { debounce } from '$lib/debounce';
  import type { QualityProfile, RequestItem, Status } from '$lib/types';

  let status: Status | null = null;
  let requests: RequestItem[] = [];
  let profiles: QualityProfile[] = [];
  let loading = true;
  let working = false;
  let errorMessage = '';
  let infoMessage = '';
  let profileSaving: Record<number, boolean> = {};

  async function loadRequests() {
    loading = true;
    errorMessage = '';
    try {
      const [statusResult, result, profileResult] = await Promise.all([api.status(), api.requests(), api.listProfiles()]);
      status = statusResult;
      requests = result.requests;
      profiles = profileResult.profiles ?? [];
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
      // Backend responds immediately with {queued: true} and syncs in a
      // background goroutine — the real seen/created counts arrive later via
      // a 'requests.sync' event (handled in onMount below).
      await api.syncRequests();
      infoMessage = 'Sync started in background';
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
      // The backend responds immediately with {queued: true} and does the
      // actual work in a background goroutine — the real processed/searched/
      // selected/failed counts arrive later via a 'library.search_pending'
      // event (handled in onMount below), not on this response. Reading
      // those fields directly off this result was always undefined.
      await api.searchPendingLibrary();
      infoMessage = 'Search queued — processing in background…';
      toastSuccess(infoMessage);
      await loadRequests();
    } catch (error) {
      errorMessage = error instanceof Error ? error.message : String(error);
      toastError(errorMessage);
    } finally {
      working = false;
    }
  }

  async function setProfile(requestID: number, nextValue: string) {
    profileSaving = { ...profileSaving, [requestID]: true };
    try {
      const parsed = nextValue ? Number(nextValue) : null;
      const profileId = parsed != null && Number.isFinite(parsed) ? parsed : null;
      await api.setRequestProfile(requestID, profileId);
      requests = requests.map((item) =>
        item.id === requestID
          ? {
              ...item,
              qualityProfileId: profileId ?? undefined,
              qualityProfileName: profileId == null ? undefined : profiles.find((p) => p.id === profileId)?.name
            }
          : item
      );
      toastSuccess(profileId == null ? 'Request profile cleared' : 'Request profile updated');
    } catch (error) {
      const detail = error instanceof Error ? error.message : String(error);
      errorMessage = detail;
      toastError(detail);
      await loadRequests();
    } finally {
      profileSaving = { ...profileSaving, [requestID]: false };
    }
  }

  const debouncedLoadRequests = debounce(() => void loadRequests(), 500);

  onMount(() => {
    void loadRequests();
    const unsubscribe = subscribeEvents((event) => {
      if (event?.kind === 'library.search_pending') {
        const e = event as Record<string, unknown>;
        toastSuccess(`Search Pending complete: processed ${e.processed}, searched ${e.searched}, selected ${e.selected}, failed ${e.failed}`);
      }
      if (event?.kind === 'requests.sync') {
        const e = event as Record<string, unknown>;
        toastSuccess(`Sync complete: seen ${e.seen ?? 0}, created ${e.created ?? 0}`);
      }
      if (!working) {
        debouncedLoadRequests();
      }
    });
    const timer = window.setInterval(() => {
      if (!working && document.visibilityState === 'visible') void loadRequests();
    }, 30000);
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
        <span>Profile</span>
        <span>Queue</span>
        <span>Created</span>
      </div>
      {#each requests as item}
        <div class="row">
          <span><strong>{item.title || item.externalId}</strong></span>
          <span>{item.requestType} · {item.mediaType}</span>
          <span>
            {#if item.libraryItemId}
              <select
                class="profile-select"
                value={item.qualityProfileId == null ? '' : String(item.qualityProfileId)}
                disabled={!!profileSaving[item.id]}
                on:change={(event) => setProfile(item.id, (event.currentTarget as HTMLSelectElement).value)}
              >
                <option value="">Default profile</option>
                {#each profiles as profile}
                  <option value={profile.id}>{profile.name}{profile.isDefault ? ' · default' : ''}</option>
                {/each}
              </select>
            {:else}
              <span class="muted">Unlinked</span>
            {/if}
          </span>
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
    grid-template-columns: minmax(0, 2fr) minmax(0, 1fr) minmax(180px, 1fr) 130px minmax(0, 1fr);
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

  .empty,
  .muted {
    color: hsl(var(--muted-foreground));
  }

  .profile-select {
    width: 100%;
    min-height: 36px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    padding: 0 10px;
    font-size: 13px;
  }

  @media (max-width: 900px) {
    .thead,
    .row {
      grid-template-columns: 1fr;
    }
  }
</style>
