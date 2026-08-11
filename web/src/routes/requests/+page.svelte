<script lang="ts">
  /**
   * Displays imported Seerr request records alongside their linked library/queue
   * state, with controls to sync from Seerr, run a pending-library search, and
   * override each request's quality profile.
   *
   * Reloads are driven by a debounced SSE event listener plus a 30s fallback
   * poll (paused when the tab isn't visible). The sync/search actions only
   * queue background jobs — their real result counts arrive later via SSE
   * events handled in onMount, not on the initial response.
   */
  import { onMount } from 'svelte';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import SearchCheck from '@lucide/svelte/icons/search-check';
  import Play from '@lucide/svelte/icons/play';
  import Button from '$lib/components/Button.svelte';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import * as Select from '$lib/components/ui/select/index.js';
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

  /** Persists a per-request quality-profile override, then mirrors it into local state; a failed save falls back to a full reload to stay in sync with the server. */
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

  /** Label shown on a row's profile trigger, mirroring what the native <option> list used to show. */
  function profileLabel(item: RequestItem): string {
    if (item.qualityProfileId == null) return 'Default profile';
    const profile = profiles.find((p) => p.id === item.qualityProfileId);
    return profile ? `${profile.name}${profile.isDefault ? ' · default' : ''}` : 'Default profile';
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

{#if errorMessage}<div class="mb-4 rounded-2xl border px-3.5 py-3 text-sm" style="border-color: hsl(var(--status-failed) / 0.28); background: hsl(var(--status-failed) / 0.12); color: hsl(var(--status-failed))">{errorMessage}</div>{/if}
{#if infoMessage}<div class="mb-4 rounded-2xl border border-primary bg-primary px-3.5 py-3 text-sm text-primary-foreground">{infoMessage}</div>{/if}
{#if status && !seerrReady}
  <div class="mb-4 rounded-2xl border px-3.5 py-3 text-sm" style="border-color: hsl(var(--status-warning) / 0.28); background: hsl(var(--status-warning) / 0.12); color: hsl(var(--status-warning))">Request sync disabled: {status.integrations.seerr.detail}.</div>
{/if}
{#if status && !hydraReady}
  <div class="mb-4 rounded-2xl border px-3.5 py-3 text-sm" style="border-color: hsl(var(--status-warning) / 0.28); background: hsl(var(--status-warning) / 0.12); color: hsl(var(--status-warning))">Pending search disabled: {status.integrations.nzbhydra2.detail}.</div>
{/if}

<Panel title="Request Feed" subtitle="Sorted by creation time descending." flush>
  {#if requests.length > 0}
    <div class="grid gap-2.5">
      <div class="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(180px,1fr)_130px_minmax(0,1fr)] items-center gap-3 px-1 text-xs uppercase text-muted-foreground max-md:hidden">
        <span>Title</span>
        <span>Type</span>
        <span>Profile</span>
        <span>Queue</span>
        <span>Created</span>
      </div>
      {#each requests as item}
        <div class="grid grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(180px,1fr)_130px_minmax(0,1fr)] items-center gap-3 rounded-2xl border border-border bg-muted/20 p-3.5 max-md:grid-cols-1 max-md:gap-2">
          <span><strong class="block">{item.title || item.externalId}</strong></span>
          <span>{item.requestType} · {item.mediaType}</span>
          <span>
            {#if item.libraryItemId}
              <Select.Root
                type="single"
                value={item.qualityProfileId == null ? '' : String(item.qualityProfileId)}
                disabled={!!profileSaving[item.id]}
                onValueChange={(value) => setProfile(item.id, value)}
              >
                <Select.Trigger class="w-full">{profileLabel(item)}</Select.Trigger>
                <Select.Content>
                  <Select.Item value="">Default profile</Select.Item>
                  {#each profiles as profile}
                    <Select.Item value={String(profile.id)}>{profile.name}{profile.isDefault ? ' · default' : ''}</Select.Item>
                  {/each}
                </Select.Content>
              </Select.Root>
            {:else}
              <span class="text-muted-foreground">Unlinked</span>
            {/if}
          </span>
          <span><StatusPill tone={item.queueState === 'available' ? 'ok' : 'neutral'}>{sentence(item.queueState)}</StatusPill></span>
          <span>{dateTime(item.createdAt)}</span>
        </div>
      {/each}
    </div>
  {:else if loading}
    <div class="text-sm text-muted-foreground">Loading requests.</div>
  {:else}
    <div class="text-sm text-muted-foreground">No requests.</div>
  {/if}
</Panel>
