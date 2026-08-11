<script lang="ts">
  /**
   * Displays connection/readiness status for every backend-dependent service
   * (NZBHydra2, usenet providers, Seerr, metadata sources, VFS mount) plus
   * live runtime metrics (NNTP connections, active streams, disk cache).
   *
   * State reloads on a debounced SSE event listener. "Probe Integrations"
   * actively re-checks connectivity and refreshes status once the probe
   * completes, rather than relying solely on cached configured/enabled flags.
   */
  import { onMount } from 'svelte';
  import Database from '@lucide/svelte/icons/database';
  import FolderTree from '@lucide/svelte/icons/folder-tree';
  import HardDrive from '@lucide/svelte/icons/hard-drive';
  import RadioTower from '@lucide/svelte/icons/radio-tower';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import Server from '@lucide/svelte/icons/server';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import Tv from '@lucide/svelte/icons/tv';
  import Zap from '@lucide/svelte/icons/zap';
  import CheckCircle2 from '@lucide/svelte/icons/check-circle-2';
  import XCircle from '@lucide/svelte/icons/x-circle';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError } from '$lib/toast';
  import { runAction } from '$lib/actions';
  import { bytes as fmtBytes } from '$lib/format';
  import { debounce } from '$lib/debounce';
  import type { IntegrationProbeReport, Status } from '$lib/types';

  type ServiceCard = {
    key: string;
    label: string;
    ok: boolean;
    detail: string;
    icon: typeof Server;
  };

  let status: Status | null = null;
  let metrics: Record<string, number> = {};
  let probeReport: IntegrationProbeReport | null = null;
  let loading = true;
  let probing = false;

  function baseURL() {
    if (typeof window === 'undefined') return 'http://localhost:8080';
    return `${window.location.protocol}//${window.location.hostname}:8080`;
  }

  async function load() {
    loading = true;
    try {
      const [nextStatus, nextMetrics] = await Promise.all([api.status(), api.metrics()]);
      status = nextStatus;
      metrics = nextMetrics;
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function runProbe() {
    await runAction(() => api.probeIntegrations(), {
      setWorking: (v) => (probing = v),
      successMessage: () => 'Probe complete',
      afterSuccess: async (result) => {
        probeReport = result;
        await load();
      }
    });
  }


  function fmtCount(value?: number) {
    return Number.isFinite(value) ? String(value) : '0';
  }

  function fmtPct(value: number, total: number) {
    if (!total) return '0%';
    return `${Math.round((value / total) * 100)}%`;
  }

  function fmtMs(ms: number) {
    if (ms < 1000) return `${ms}ms`;
    return `${(ms / 1000).toFixed(1)}s`;
  }

  $: usenet = ((status?.settings.usenet as Record<string, unknown> | undefined) ?? {});
  $: providers = Array.isArray(usenet.providers) ? usenet.providers as Record<string, unknown>[] : [];
  $: totalConfiguredConnections = providers.reduce((sum, provider) => sum + Number(provider.maxConnections ?? 0), 0);
  $: runtimeCards = status ? [
    { label: 'Services healthy', value: `${serviceCards.filter((row) => row.ok).length}/${serviceCards.length}`, tone: 'ok' },
    { label: 'Active streams', value: fmtCount(metrics.active_streams), tone: 'primary' },
    { label: 'NNTP active', value: fmtCount(metrics.active_nntp_connections), tone: 'warn' },
    { label: 'Disk cache used', value: fmtBytes(metrics.disk_cache_used_bytes ?? 0), tone: 'neutral' }
  ] : [];

  $: serviceCards = !status ? [] : [
    {
      key: 'backend',
      label: 'Backend API',
      ok: status.healthy,
      detail: status.healthy ? 'reachable' : 'unhealthy',
      icon: Server
    },
    {
      key: 'nzbhydra2',
      label: 'NZBHydra2',
      ok: status.integrations.nzbhydra2.enabled && status.integrations.nzbhydra2.configured,
      detail: status.integrations.nzbhydra2.detail || 'not configured',
      icon: RadioTower
    },
    {
      key: 'usenet',
      label: 'Usenet',
      ok: status.integrations.usenet.enabled && status.integrations.usenet.configured,
      detail: `${providers.length} provider(s) · ${totalConfiguredConnections} max conn`,
      icon: HardDrive
    },
    {
      key: 'seerr',
      label: 'Seerr',
      ok: status.integrations.seerr.enabled && status.integrations.seerr.configured,
      detail: status.integrations.seerr.detail || 'not configured',
      icon: Tv
    },
    {
      key: 'metadata',
      label: 'Metadata',
      ok: status.integrations.tmdb.configured || status.integrations.tvdb.configured,
      detail: `TMDB ${status.integrations.tmdb.configured ? 'ok' : 'off'} · TVDB ${status.integrations.tvdb.configured ? 'ok' : 'off'}`,
      icon: ShieldCheck
    },
    {
      key: 'vfs',
      label: 'VFS mount',
      ok: true,
      detail: status.fuseMountPath,
      icon: FolderTree
    }
  ];

  onMount(() => {
    void load();
    const debouncedLoad = debounce(() => void load(), 500);
    return subscribeEvents(() => {
      if (!probing) debouncedLoad();
    });
  });
</script>

<svelte:head><title>Services — Drakkar</title></svelte:head>

<PageHeader title="Services" subtitle="Connection and runtime status for the services Drakkar depends on.">
  <Button kind="secondary" on:click={load} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <Button kind="primary" on:click={runProbe} disabled={probing}>
    <Zap size={14} />
    {probing ? 'Probing…' : 'Probe Integrations'}
  </Button>
</PageHeader>

{#if status}
  <section class="mb-5 grid grid-cols-2 gap-3.5 md:grid-cols-4">
    {#each runtimeCards as card}
      <div class="rounded-xl border border-border bg-white/[0.03] px-4 py-3.5">
        <div
          class="text-2xl font-bold leading-none"
          style={card.tone === 'ok' ? 'color: hsl(var(--status-available))' : card.tone === 'warn' ? 'color: hsl(var(--status-warning))' : card.tone === 'primary' ? 'color: var(--primary)' : 'color: var(--foreground)'}
        >{card.value}</div>
        <div class="mt-2 text-sm text-muted-foreground">{card.label}</div>
      </div>
    {/each}
  </section>

  <Panel title="Connected Services" subtitle="Reference-style readiness view, but bound to Drakkar live data.">
    <div slot="actions">
      <StatusPill tone={status.healthy ? 'ok' : 'warn'}>{status.healthy ? 'Healthy' : 'Attention needed'}</StatusPill>
    </div>
    <div class="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-3.5">
      {#each serviceCards as row}
        <div class="rounded-xl border border-border bg-white/[0.03] p-4.5">
          <div class="flex items-start justify-between gap-3">
            <div class="flex items-start gap-3">
              <div class="grid size-11 shrink-0 place-items-center rounded-2xl bg-white/[0.06] text-primary">
                <svelte:component this={row.icon} size={18} />
              </div>
              <div>
                <div class="font-semibold">{row.label}</div>
                <div class="mt-2 text-sm text-muted-foreground">{row.detail}</div>
              </div>
            </div>
            <span style={row.ok ? 'color: hsl(var(--status-available))' : 'color: hsl(var(--status-failed))'}>
              <svelte:component this={row.ok ? CheckCircle2 : XCircle} size={18} />
            </span>
          </div>
          <div class="mt-3.5">
            <StatusPill tone={row.ok ? 'ok' : 'warn'}>{row.ok ? 'Ready' : 'Needs config'}</StatusPill>
          </div>
        </div>
      {/each}
    </div>
  </Panel>

  <div class="my-5 grid grid-cols-1 gap-5 lg:grid-cols-[minmax(0,1.1fr)_minmax(0,0.9fr)]">
    <Panel title="Runtime" subtitle="Current pool, stream, and cache posture.">
      <div class="mb-3.5 grid grid-cols-2 gap-3.5">
        <div class="rounded-xl border border-border bg-white/[0.03] p-4">
          <div class="text-xl font-bold leading-none">{fmtCount(metrics.active_nntp_connections)}</div>
          <div class="mt-2 text-sm text-muted-foreground">Active NNTP</div>
        </div>
        <div class="rounded-xl border border-border bg-white/[0.03] p-4">
          <div class="text-xl font-bold leading-none">{fmtCount(metrics.idle_nntp_connections)}</div>
          <div class="mt-2 text-sm text-muted-foreground">Idle NNTP</div>
        </div>
        <div class="rounded-xl border border-border bg-white/[0.03] p-4">
          <div class="text-xl font-bold leading-none">{fmtCount(metrics.queued_background_fetches)}</div>
          <div class="mt-2 text-sm text-muted-foreground">Queued background</div>
        </div>
        <div class="rounded-xl border border-border bg-white/[0.03] p-4">
          <div class="text-xl font-bold leading-none">{fmtCount(metrics.active_streams)}</div>
          <div class="mt-2 text-sm text-muted-foreground">Streaming sessions</div>
        </div>
      </div>
      <div class="grid gap-2.5">
        <div class="flex justify-between gap-3 border-t border-white/5 py-2.5 text-sm"><span>Max download connections</span><strong>{usenet.maxDownloadConnections ?? 0}</strong></div>
        <div class="flex justify-between gap-3 border-t border-white/5 py-2.5 text-sm"><span>Streaming priority</span><strong>{usenet.streamingPriorityPercent ?? 0}%</strong></div>
        <div class="flex justify-between gap-3 border-t border-white/5 py-2.5 text-sm"><span>Article buffer size</span><strong>{usenet.articleBufferSize ?? 0}</strong></div>
        <div class="flex justify-between gap-3 border-t border-white/5 py-2.5 text-sm"><span>Read-ahead limit</span><strong>{fmtBytes(status.readAheadLimitBytes)}</strong></div>
      </div>
    </Panel>

    <Panel title="Providers" subtitle="Configured provider pool and subtitle integrations.">
      <div class="grid gap-2.5">
        {#each providers as provider}
          <div class="flex items-start justify-between gap-3 rounded-xl border border-border bg-white/[0.03] px-4 py-3.5">
            <div>
              <div class="font-semibold">{String(provider.name ?? 'Usenet')}</div>
              <div class="mt-2 text-sm text-muted-foreground">{String(provider.host ?? '')}</div>
            </div>
            <StatusPill tone={provider.enabled ? 'ok' : 'neutral'}>
              {provider.enabled ? `${provider.maxConnections ?? 0} conn` : 'disabled'}
            </StatusPill>
          </div>
        {/each}
        {#if providers.length === 0}
          <div class="text-sm text-muted-foreground">No usenet providers configured.</div>
        {/if}
      </div>
      <div class="mt-3.5 rounded-xl border border-border bg-white/[0.03] px-4 py-3.5">
        <div class="font-semibold">Subtitle providers</div>
        <div class="mt-2.5 flex flex-wrap items-start gap-3">
          {#each Object.entries(status.integrations.subtitleProviders) as [name, info]}
            <StatusPill tone={info.configured ? 'ok' : info.enabled ? 'warn' : 'neutral'}>
              {name}
            </StatusPill>
          {/each}
        </div>
      </div>
    </Panel>
  </div>

  {#if probeReport}
    <Panel title="Last Probe" subtitle="Live integration probe results.">
      <div class="grid grid-cols-[repeat(auto-fit,minmax(240px,1fr))] gap-3.5">
        {#each probeReport.results as result}
          <div class="flex items-start justify-between gap-3 rounded-xl border border-border bg-white/[0.03] px-4 py-3.5">
            <div>
              <div class="font-semibold">{result.name}</div>
              <div class="mt-2 text-sm text-muted-foreground">{result.detail || (result.ok ? 'reachable' : 'unreachable')}</div>
            </div>
            <div class="flex shrink-0 items-center gap-3">
              <StatusPill tone={result.ok ? 'ok' : 'danger'}>{result.ok ? 'OK' : 'Fail'}</StatusPill>
              <span class="font-mono text-xs text-muted-foreground">{fmtMs(result.durationMs)}</span>
            </div>
          </div>
        {/each}
      </div>
    </Panel>
  {/if}
{:else}
  <Panel title="Services" subtitle="Loading live service state.">
    <div class="text-sm text-muted-foreground">{loading ? 'Loading services…' : 'No status available.'}</div>
  </Panel>
{/if}
