<script lang="ts">
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
  import { toastError, toastSuccess } from '$lib/toast';
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
    probing = true;
    try {
      probeReport = await api.probeIntegrations();
      toastSuccess('Probe complete');
      await load();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      probing = false;
    }
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
  <section class="summary-grid">
    {#each runtimeCards as card}
      <div class="summary-card">
        <div class={`summary-value ${card.tone}`}>{card.value}</div>
        <div class="summary-label">{card.label}</div>
      </div>
    {/each}
  </section>

  <Panel title="Connected Services" subtitle="Reference-style readiness view, but bound to Drakkar live data.">
    <div slot="actions">
      <StatusPill tone={status.healthy ? 'ok' : 'warn'}>{status.healthy ? 'Healthy' : 'Attention needed'}</StatusPill>
    </div>
    <div class="service-grid">
      {#each serviceCards as row}
        <div class="service-card">
          <div class="service-head">
            <div class="service-ident">
              <div class="icon-shell">
                <svelte:component this={row.icon} size={18} />
              </div>
              <div>
                <div class="service-name">{row.label}</div>
                <div class="service-detail">{row.detail}</div>
              </div>
            </div>
            <span class:ok={row.ok} class:fail={!row.ok}>
              <svelte:component this={row.ok ? CheckCircle2 : XCircle} size={18} />
            </span>
          </div>
          <div class="service-foot">
            <StatusPill tone={row.ok ? 'ok' : 'warn'}>{row.ok ? 'Ready' : 'Needs config'}</StatusPill>
          </div>
        </div>
      {/each}
    </div>
  </Panel>

  <div class="two-col">
    <Panel title="Runtime" subtitle="Current pool, stream, and cache posture.">
      <div class="runtime-grid">
        <div class="runtime-tile">
          <div class="runtime-value">{fmtCount(metrics.active_nntp_connections)}</div>
          <div class="runtime-label">Active NNTP</div>
        </div>
        <div class="runtime-tile">
          <div class="runtime-value">{fmtCount(metrics.idle_nntp_connections)}</div>
          <div class="runtime-label">Idle NNTP</div>
        </div>
        <div class="runtime-tile">
          <div class="runtime-value">{fmtCount(metrics.queued_background_fetches)}</div>
          <div class="runtime-label">Queued background</div>
        </div>
        <div class="runtime-tile">
          <div class="runtime-value">{fmtCount(metrics.active_streams)}</div>
          <div class="runtime-label">Streaming sessions</div>
        </div>
      </div>
      <div class="policy-list">
        <div class="policy-row"><span>Max download connections</span><strong>{usenet.maxDownloadConnections ?? 0}</strong></div>
        <div class="policy-row"><span>Streaming priority</span><strong>{usenet.streamingPriorityPercent ?? 0}%</strong></div>
        <div class="policy-row"><span>Article buffer size</span><strong>{usenet.articleBufferSize ?? 0}</strong></div>
        <div class="policy-row"><span>Read-ahead limit</span><strong>{fmtBytes(status.readAheadLimitBytes)}</strong></div>
      </div>
    </Panel>

    <Panel title="Providers" subtitle="Configured provider pool and subtitle integrations.">
      <div class="provider-list">
        {#each providers as provider}
          <div class="provider-row">
            <div>
              <div class="provider-name">{String(provider.name ?? 'Usenet')}</div>
              <div class="provider-detail">{String(provider.host ?? '')}</div>
            </div>
            <StatusPill tone={provider.enabled ? 'ok' : 'neutral'}>
              {provider.enabled ? `${provider.maxConnections ?? 0} conn` : 'disabled'}
            </StatusPill>
          </div>
        {/each}
        {#if providers.length === 0}
          <div class="empty-state">No usenet providers configured.</div>
        {/if}
      </div>
      <div class="subtitle-box">
        <div class="subtitle-head">Subtitle providers</div>
        <div class="subtitle-row">
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
      <div class="probe-grid">
        {#each probeReport.results as result}
          <div class="probe-row">
            <div>
              <div class="probe-name">{result.name}</div>
              <div class="probe-detail">{result.detail || (result.ok ? 'reachable' : 'unreachable')}</div>
            </div>
            <div class="probe-meta">
              <StatusPill tone={result.ok ? 'ok' : 'danger'}>{result.ok ? 'OK' : 'Fail'}</StatusPill>
              <span class="mono probe-time">{fmtMs(result.durationMs)}</span>
            </div>
          </div>
        {/each}
      </div>
    </Panel>
  {/if}
{:else}
  <Panel title="Services" subtitle="Loading live service state.">
    <div class="empty-state">{loading ? 'Loading services…' : 'No status available.'}</div>
  </Panel>
{/if}

<style>
  .summary-grid,
  .service-grid,
  .runtime-grid,
  .probe-grid {
    display: grid;
    gap: 14px;
  }

  .summary-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
    margin-bottom: 20px;
  }

  .summary-card,
  .service-card,
  .runtime-tile,
  .probe-row,
  .provider-row,
  .subtitle-box {
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 18px;
    background: hsl(0 0% 100% / 0.03);
  }

  .summary-card {
    padding: 18px 20px;
  }

  .summary-value {
    font-size: 2rem;
    font-weight: 700;
    line-height: 1;
  }

  .summary-value.ok { color: hsl(141 80% 68%); }
  .summary-value.primary { color: hsl(var(--primary)); }
  .summary-value.warn { color: hsl(47 100% 77%); }
  .summary-value.neutral { color: hsl(var(--foreground)); }

  .summary-label,
  .service-detail,
  .runtime-label,
  .provider-detail,
  .probe-detail,
  .empty-state {
    margin-top: 8px;
    color: hsl(var(--muted-foreground));
    font-size: 13px;
  }

  .service-grid {
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  }

  .service-card {
    padding: 18px;
  }

  .service-head,
  .service-ident,
  .provider-row,
  .probe-row,
  .probe-meta,
  .subtitle-row {
    display: flex;
    align-items: flex-start;
    gap: 12px;
  }

  .service-head,
  .provider-row,
  .probe-row {
    justify-content: space-between;
  }

  .icon-shell {
    display: grid;
    place-items: center;
    width: 44px;
    height: 44px;
    border-radius: 14px;
    background: hsl(0 0% 100% / 0.06);
    color: hsl(var(--primary));
    flex-shrink: 0;
  }

  .service-name,
  .provider-name,
  .probe-name,
  .subtitle-head {
    font-weight: 600;
  }

  .service-foot {
    margin-top: 14px;
  }

  .ok { color: hsl(141 80% 68%); }
  .fail { color: hsl(0 96% 82%); }

  .two-col {
    display: grid;
    grid-template-columns: minmax(0, 1.1fr) minmax(0, 0.9fr);
    gap: 20px;
    margin: 20px 0;
  }

  .runtime-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
    margin-bottom: 14px;
  }

  .runtime-tile {
    padding: 16px;
  }

  .runtime-value {
    font-size: 1.8rem;
    font-weight: 700;
    line-height: 1;
  }

  .policy-list {
    display: grid;
    gap: 10px;
  }

  .policy-row {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    padding: 10px 0;
    border-top: 1px solid hsl(0 0% 100% / 0.05);
    font-size: 13px;
  }

  .provider-list {
    display: grid;
    gap: 10px;
  }

  .provider-row,
  .probe-row {
    padding: 14px 16px;
  }

  .subtitle-box {
    margin-top: 14px;
    padding: 14px 16px;
  }

  .subtitle-row {
    flex-wrap: wrap;
    margin-top: 10px;
  }

  .probe-grid {
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  }

  .probe-meta {
    align-items: center;
    flex-shrink: 0;
  }

  .probe-time {
    color: hsl(var(--muted-foreground));
    font-size: 12px;
  }

  @media (max-width: 980px) {
    .summary-grid,
    .two-col {
      grid-template-columns: 1fr;
    }
  }

  @media (max-width: 640px) {
    .runtime-grid {
      grid-template-columns: 1fr 1fr;
    }
  }
</style>
