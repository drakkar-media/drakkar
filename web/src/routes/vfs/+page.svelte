<script lang="ts">
  import { onMount } from 'svelte';
  import Copy from '@lucide/svelte/icons/copy';
  import Folder from '@lucide/svelte/icons/folder';
  import File from '@lucide/svelte/icons/file';
  import RefreshCw from '@lucide/svelte/icons/refresh-cw';
  import ChevronRight from '@lucide/svelte/icons/chevron-right';
  import PageHeader from '$lib/components/PageHeader.svelte';
  import Panel from '$lib/components/Panel.svelte';
  import Button from '$lib/components/Button.svelte';
  import StatusPill from '$lib/components/StatusPill.svelte';
  import { api } from '$lib/api';
  import { toastError, toastSuccess } from '$lib/toast';

  type VFSEntry = { name: string; path: string; isDir: boolean; size: number };
  type StreamItem = { sessionID?: string; sessionId?: string; fileName?: string; filePath?: string; currentOffset?: number; fileSize?: number; fileSizeBytes?: number };

  let currentPath = '/';
  let entries: VFSEntry[] = [];
  let streams: StreamItem[] = [];
  let metrics: Record<string, number> = {};
  let loading = false;

  function baseURL() {
    if (typeof window === 'undefined') return 'http://localhost:8080';
    return `${window.location.protocol}//${window.location.hostname}:8080`;
  }

  async function browse(path: string) {
    loading = true;
    try {
      const [listing, nextMetrics, nextStreams] = await Promise.all([
        fetch(`${baseURL()}/api/vfs?path=${encodeURIComponent(path)}`).then(async (r) => {
          if (!r.ok) throw new Error(`${r.status} ${r.statusText}`);
          return r.json();
        }),
        api.metrics(),
        api.streams()
      ]);
      entries = listing.entries ?? [];
      currentPath = path;
      metrics = nextMetrics;
      streams = nextStreams.sessions ?? [];
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    } finally {
      loading = false;
    }
  }

  function crumbs() {
    const parts = currentPath.split('/').filter(Boolean);
    const result = [{ label: 'vfs', path: '/' }];
    let acc = '';
    for (const part of parts) {
      acc += `/${part}`;
      result.push({ label: part, path: acc });
    }
    return result;
  }

  function goUp() {
    const parts = currentPath.split('/').filter(Boolean);
    parts.pop();
    void browse(parts.length ? `/${parts.join('/')}` : '/');
  }

  function fmtBytes(bytes: number) {
    if (!bytes) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit += 1;
    }
    return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
  }

  async function copy(text: string, label: string) {
    try {
      await navigator.clipboard.writeText(text);
      toastSuccess(`${label} copied`);
    } catch (err) {
      toastError(err instanceof Error ? err.message : String(err));
    }
  }

  $: sorted = [...entries].sort((a, b) => (Number(b.isDir) - Number(a.isDir)) || a.name.localeCompare(b.name));

  onMount(() => {
    void browse('/');
  });
</script>

<svelte:head><title>VFS — Drakkar</title></svelte:head>

<PageHeader title="VFS Browser" subtitle="Browse the mounted virtual library, inspect path shape, and check active stream sessions.">
  <Button kind="secondary" on:click={() => browse(currentPath)} disabled={loading}>
    <RefreshCw size={14} />
    Refresh
  </Button>
</PageHeader>

<div class="main-grid">
  <div class="left-col">
    <Panel title="Path" subtitle="Current mounted directory view.">
      <div class="breadcrumb">
        {#each crumbs() as crumb, i}
        {#if i > 0}<span class="sep"><ChevronRight size={12} /></span>{/if}
          <button class:active={crumb.path === currentPath} on:click={() => browse(crumb.path)}>{crumb.label}</button>
        {/each}
      </div>

      <div class="table-wrap">
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Type</th>
              <th>Size</th>
              <th>Action</th>
            </tr>
          </thead>
          <tbody>
            {#if currentPath !== '/'}
              <tr>
                <td>
                  <button class="path-btn" on:click={goUp}>
                    <Folder size={14} /> ..
                  </button>
                </td>
                <td><StatusPill tone="neutral">folder</StatusPill></td>
                <td class="muted">—</td>
                <td class="muted">parent</td>
              </tr>
            {/if}
            {#each sorted as entry}
              <tr>
                <td>
                  {#if entry.isDir}
                    <button class="path-btn" on:click={() => browse(entry.path)}>
                      <Folder size={14} /> {entry.name}
                    </button>
                  {:else}
                    <div class="path-btn static">
                      <File size={14} /> {entry.name}
                    </div>
                  {/if}
                </td>
                <td><StatusPill tone={entry.isDir ? 'ok' : 'neutral'}>{entry.isDir ? 'folder' : 'file'}</StatusPill></td>
                <td class="muted">{entry.isDir ? '—' : fmtBytes(entry.size)}</td>
                <td>
                  <button class="copy-btn" on:click={() => copy(`/mnt/drakkar/vfs${entry.path === '/' ? '' : entry.path}`, 'VFS path')}>
                    <Copy size={13} /> Copy path
                  </button>
                </td>
              </tr>
            {/each}
            {#if !loading && sorted.length === 0}
              <tr><td colspan="4" class="empty-state">Directory empty.</td></tr>
            {/if}
          </tbody>
        </table>
      </div>
    </Panel>
  </div>

  <div class="right-col">
    <Panel title="Mount" subtitle="Mounted VFS and cache posture.">
      <div class="meta-list">
        <div class="meta-row"><span>Mount path</span><strong>/mnt/drakkar/vfs</strong></div>
        <div class="meta-row"><span>Active streams</span><strong>{metrics.active_streams ?? 0}</strong></div>
        <div class="meta-row"><span>Active NNTP</span><strong>{metrics.active_nntp_connections ?? 0}</strong></div>
        <div class="meta-row"><span>Idle NNTP</span><strong>{metrics.idle_nntp_connections ?? 0}</strong></div>
        <div class="meta-row"><span>Cache used</span><strong>{fmtBytes(metrics.disk_cache_used_bytes ?? 0)}</strong></div>
      </div>
    </Panel>

    <Panel title="Streams" subtitle="Current sessions reading from the mounted library.">
      {#if streams.length > 0}
        <div class="stream-list">
          {#each streams as stream}
            {@const sid = stream.sessionId ?? stream.sessionID ?? ''}
            <div class="stream-card">
              <div class="stream-title">{stream.fileName ?? stream.filePath ?? 'stream'}</div>
              <div class="stream-sub mono">{sid.slice(0, 24)}</div>
              <div class="stream-meta">
                <StatusPill tone="ok">offset {fmtBytes(stream.currentOffset ?? 0)}</StatusPill>
                <StatusPill tone="neutral">size {fmtBytes(stream.fileSizeBytes ?? stream.fileSize ?? 0)}</StatusPill>
                {#if sid}
                  <button class="stop-btn" on:click={async () => {
                    try { await api.stopStream(sid); toastSuccess('Stream stopped'); void browse(currentPath); }
                    catch (e) { toastError(e instanceof Error ? e.message : String(e)); }
                  }}>Stop</button>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="empty-state">No active VFS stream sessions.</div>
      {/if}
    </Panel>
  </div>
</div>

<style>
  .main-grid {
    display: grid;
    grid-template-columns: minmax(0, 1.25fr) minmax(320px, 0.75fr);
    gap: 20px;
  }

  .left-col,
  .right-col {
    display: grid;
    gap: 20px;
    align-content: start;
  }

  .breadcrumb {
    display: flex;
    align-items: center;
    gap: 6px;
    flex-wrap: wrap;
    margin-bottom: 16px;
  }

  .breadcrumb button,
  .copy-btn,
  .path-btn {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-height: 34px;
    padding: 0 10px;
    border-radius: 12px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    background: hsl(0 0% 100% / 0.04);
    color: hsl(var(--foreground));
    cursor: pointer;
  }

  .breadcrumb button.active {
    background: hsl(var(--primary));
    border-color: hsl(var(--primary));
    color: hsl(var(--primary-foreground));
  }

  .sep { color: hsl(var(--muted-foreground)); }
  .path-btn.static { cursor: default; }

  .table-wrap { overflow-x: auto; }

  table {
    width: 100%;
    min-width: 720px;
    border-collapse: collapse;
  }

  th, td {
    padding: 14px 10px;
    border-bottom: 1px solid hsl(0 0% 100% / 0.05);
    text-align: left;
    vertical-align: top;
  }

  th {
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.18em;
    color: hsl(var(--muted-foreground));
  }

  .muted,
  .empty-state,
  .stream-sub {
    color: hsl(var(--muted-foreground));
  }

  .meta-list,
  .stream-list {
    display: grid;
    gap: 10px;
  }

  .meta-row,
  .stream-card {
    padding: 14px 16px;
    border: 1px solid hsl(0 0% 100% / 0.08);
    border-radius: 16px;
    background: hsl(0 0% 100% / 0.03);
  }

  .meta-row {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    font-size: 13px;
  }

  .meta-row span {
    color: hsl(var(--muted-foreground));
  }

  .stream-title {
    font-weight: 600;
    word-break: break-word;
  }

  .stream-meta {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin-top: 10px;
    align-items: center;
  }

  .stop-btn {
    padding: 3px 10px;
    border-radius: 8px;
    border: 1px solid hsl(0 72% 51% / 0.3);
    background: hsl(0 72% 51% / 0.12);
    color: hsl(0 96% 82%);
    font-size: 12px;
    cursor: pointer;
    transition: background 0.12s;
  }
  .stop-btn:hover { background: hsl(0 72% 51% / 0.25); }

  @media (max-width: 980px) {
    .main-grid {
      grid-template-columns: 1fr;
    }
  }
</style>
