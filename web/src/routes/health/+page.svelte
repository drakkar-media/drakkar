<script lang="ts">
  import { onMount } from 'svelte';
  import HeartPulse from '@lucide/svelte/icons/heart-pulse';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import ShieldCheck from '@lucide/svelte/icons/shield-check';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api, subscribeEvents } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';

  type HealthSummary = { total: number; checked: number; healthy: number; neverChecked: number };
  type HealthEntry = {
    id: number;
    libraryItemId: number;
    libraryPath: string;
    targetPath: string;
    createdAt: string;
    lastCheckedAt?: string;
    healthOk?: boolean;
  };

  let summary: HealthSummary | null = null;
  let entries: HealthEntry[] = [];
  let loading = true;
  let checking = false;

  async function load() {
    loading = true;
    try {
      const [nextSummary, nextEntries] = await Promise.all([api.healthSummary(), api.healthEntries()]);
      summary = nextSummary;
      entries = nextEntries.items ?? [];
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  async function runCheck() {
    checking = true;
    try {
      const result = await api.runHealthCheck();
      toastSuccess(`Checked ${result.checked} — ${result.healthy} healthy`);
      await load();
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      checking = false;
    }
  }

  function fmtDate(value?: string, fallback = 'Never') {
    if (!value) return fallback;
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return fallback;
    return date.toLocaleString('en-GB', {
      month: 'short',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    });
  }

  function shortName(path: string) {
    const parts = path.split('/');
    return parts[parts.length - 1] || path;
  }

  $: checked = summary?.checked ?? 0;
  $: healthy = summary?.healthy ?? 0;
  $: broken = checked - healthy;
  $: healthyPct = checked > 0 ? Math.round((healthy / checked) * 100) : 0;

  onMount(() => {
    void load();
    return subscribeEvents(() => {
      if (!checking) void load();
    });
  });
</script>

<svelte:head><title>Health — Drakkar</title></svelte:head>

<PageHeader title="Health" subtitle="Health-check outcomes and schedule for published media.">
  <Button kind="secondary" on:click={load} disabled={loading || checking}>
    <RefreshCw size={14} />
    Refresh
  </Button>
  <Button kind="primary" on:click={runCheck} disabled={loading || checking}>
    <ShieldCheck size={14} />
    {checking ? 'Running…' : 'Run Health Check'}
  </Button>
</PageHeader>

{#if summary}
  <section class="stats-grid">
    <div class="stat-card">
      <div class="stat-value">{summary.total}</div>
      <div class="stat-label">Total published</div>
    </div>
    <div class="stat-card">
      <div class="stat-value ok">{healthy}</div>
      <div class="stat-label">Healthy ({healthyPct}%)</div>
      <div class="bar"><div class="fill ok" style={`width:${healthyPct}%`}></div></div>
    </div>
    <div class="stat-card">
      <div class="stat-value warn">{summary.neverChecked}</div>
      <div class="stat-label">Never checked</div>
    </div>
    <div class="stat-card">
      <div class="stat-value danger">{broken}</div>
      <div class="stat-label">Broken symlinks</div>
    </div>
  </section>

  {#if summary.neverChecked > 0}
    <div class="attention">
      <div class="attention-title"><HeartPulse size={16} /> Attention</div>
      <ul>
        <li>{summary.neverChecked} item(s) never health-checked yet.</li>
        <li>Run a check now if you want immediate verification.</li>
        <li>Normal publish flow updates health state automatically.</li>
      </ul>
    </div>
  {/if}

  <Panel title="Schedule" subtitle="Reference-style schedule table, backed by Drakkar health rows.">
    <div slot="actions">
      <StatusPill tone={broken > 0 ? 'warn' : 'ok'}>{entries.length} item(s)</StatusPill>
    </div>
    {#if entries.length > 0}
      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Created</th>
              <th>Last Check</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {#each entries as entry}
              <tr>
                <td>
                  <div class="row-title">{shortName(entry.libraryPath)}</div>
                  <div class="row-sub">{entry.libraryPath}</div>
                </td>
                <td>{fmtDate(entry.createdAt, 'Unknown')}</td>
                <td>{fmtDate(entry.lastCheckedAt)}</td>
                <td>
                  {#if entry.healthOk === true}
                    <StatusPill tone="ok">Healthy</StatusPill>
                  {:else if entry.healthOk === false}
                    <StatusPill tone="danger">Broken</StatusPill>
                  {:else}
                    <StatusPill tone="warn">ASAP</StatusPill>
                  {/if}
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
    {:else}
      <div class="empty-state">{loading ? 'Loading health entries…' : 'No published media yet.'}</div>
    {/if}
  </Panel>
{/if}

<style>
  .stats-grid {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: 14px;
    margin-bottom: 18px;
  }

  .stat-card,
  .attention {
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 20px;
    background: hsl(var(--card) / 0.82);
  }

  .stat-card {
    padding: 22px;
  }

  .stat-value {
    font-size: 2rem;
    font-weight: 700;
    line-height: 1;
  }

  .stat-value.ok { color: hsl(141 80% 68%); }
  .stat-value.warn { color: hsl(47 100% 77%); }
  .stat-value.danger { color: hsl(0 96% 82%); }
  .stat-label,
  .row-sub,
  .empty-state {
    margin-top: 8px;
    font-size: 13px;
    color: hsl(var(--muted-foreground));
  }

  .bar {
    margin-top: 12px;
    height: 8px;
    border-radius: 999px;
    background: hsl(0 0% 100% / 0.06);
    overflow: hidden;
  }

  .fill {
    height: 100%;
    border-radius: 999px;
    background: hsl(var(--primary));
  }

  .fill.ok { background: hsl(141 80% 68%); }

  .attention {
    padding: 16px 18px;
    margin-bottom: 18px;
    border-color: hsl(43 96% 44% / 0.28);
    background: hsl(43 96% 44% / 0.08);
  }

  .attention-title {
    display: flex;
    align-items: center;
    gap: 8px;
    font-weight: 700;
    color: hsl(47 100% 77%);
  }

  .attention ul {
    margin: 10px 0 0;
    padding-left: 18px;
    color: hsl(var(--foreground));
  }

  .table-wrap {
    overflow-x: auto;
  }

  table {
    width: 100%;
    border-collapse: collapse;
    min-width: 720px;
  }

  th,
  td {
    text-align: left;
    padding: 14px 10px;
    border-bottom: 1px solid hsl(0 0% 100% / 0.05);
    vertical-align: top;
  }

  th {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.18em;
    color: hsl(var(--muted-foreground));
  }

  .row-title {
    font-weight: 600;
  }

  @media (max-width: 900px) {
    .stats-grid {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }
  }
</style>
